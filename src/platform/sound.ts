// Sound effects for the TUI. Clips are decoded and shaped once, then rendered into a few variants with slightly
// different pitch and tempo, so a play does no audio work. On macOS a helper process keeps one audio engine running
// and plays the variants from memory. On Linux and Windows frost mixes the variants itself and streams them to one
// long-running output, pacat or winmm's waveOut. When none of these start, the variants are written once to a
// private temporary folder and each play runs the platform's player. FROST_NO_SOUND turns it all off.
import { spawn } from 'node:child_process';
import { mkdir, mkdtemp, writeFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import type { Readable, Writable } from 'node:stream';
import { run, type Runner } from './command.js';

export const sampleRate = 44100;

// A WAV clip and how to shape it. pitch and tempo are random jitter amounts, cut and decay are
// milliseconds, shift is a resampling factor, ring is a frequency in Hz and crush a bit depth.
export interface Clip {
  wav: Buffer;
  pitch?: number;
  tempo?: number;
  volume?: number;
  cut?: number;
  shift?: number;
  ring?: number;
  crush?: number;
  decay?: number;
}

// Time stretching overlaps 1024-sample Hann-windowed grains every 512 samples, searching up to
// 256 samples either way for the best fit.
const grain = 1024;
const hop = 512;
const tolerance = 256;
const window = Float32Array.from({ length: grain }, (_, i) => 0.5 - 0.5 * Math.cos((2 * Math.PI * i) / grain));

// A random factor between 1 - amount and 1 + amount, most likely near 1.
function jitter(amount = 0, random = Math.random): number {
  return 1 + amount * (random() - random());
}

// Linear interpolation. A step above 1 shortens the clip and raises its pitch. Math.fround keeps
// the arithmetic in 32-bit floats.
export function resample(s: Float32Array, step: number): Float32Array {
  if (!s.length || step <= 0) return s;
  if (Math.abs(step - 1) < 1e-6) return s.slice();
  const out = new Float32Array(Math.trunc(s.length / step));
  for (let i = 0; i < out.length; i++) {
    const pos = i * step;
    const j = Math.trunc(pos);
    const frac = Math.fround(pos - j);
    out[i] =
      j + 1 >= s.length ? s[s.length - 1] : Math.fround(s[j] * Math.fround(1 - frac)) + Math.fround(s[j + 1] * frac);
  }
  return out;
}

// Finds the input offset near nominal whose start best matches the audio that naturally follows
// the previous grain. The correlation uses every second sample.
function bestMatch(s: Float32Array, natural: number, nominal: number, last: number): number {
  if (natural + hop > s.length) return nominal;
  let best = nominal;
  let bestScore = -Infinity;
  for (let c = Math.max(0, nominal - tolerance); c <= Math.min(last, nominal + tolerance); c++) {
    let score = 0;
    for (let j = 0; j < hop; j += 2) score += Math.fround(s[c + j] * s[natural + j]);
    if (score > bestScore) {
      best = c;
      bestScore = score;
    }
  }
  return best;
}

// Changes the length by factor without changing pitch, using waveform-similarity overlap-add.
// Clips too short for that come back unchanged.
function stretch(s: Float32Array, factor: number): Float32Array {
  if (factor <= 0 || Math.abs(factor - 1) < 1e-3 || s.length < 2 * grain + tolerance) return s;
  const n = Math.trunc(s.length * factor);
  const out = new Float32Array(n + grain);
  const weight = new Float32Array(n + grain);
  const last = s.length - grain;
  let prev = 0;
  for (let o = 0; o < n; o += hop) {
    let input = Math.min(Math.trunc(o / factor), last);
    if (o > 0) input = bestMatch(s, prev + hop, input, last);
    for (let j = 0; j < grain; j++) {
      out[o + j] += Math.fround(s[input + j] * window[j]);
      weight[o + j] += window[j];
    }
    prev = input;
  }
  for (let i = 0; i < n; i++) if (weight[i] > 1e-3) out[i] /= weight[i];
  return out.subarray(0, n);
}

// Raises pitch by the pitch factor and speeds playback by the tempo factor.
export function vary(s: Float32Array, pitch: number, tempo: number): Float32Array {
  return pitch <= 0 || tempo <= 0 ? s : resample(stretch(s, pitch / tempo), pitch);
}

// Shortens a clip to the given length with a 20 ms linear fade-out.
export function cut(s: Float32Array, milliseconds = 0): Float32Array {
  const n = Math.trunc((milliseconds / 1000) * sampleRate);
  if (milliseconds <= 0 || n >= s.length) return s;
  const out = s.slice(0, n);
  const fade = Math.min(n, Math.trunc(0.02 * sampleRate));
  for (let i = 0; i < fade; i++) out[n - fade + i] *= (fade - i) / fade;
  return out;
}

// Applies a clip's pitch shift, ring modulation, bit crushing and exponential decay, in that order.
export function effects(s: Float32Array, clip: Clip): Float32Array {
  const out = clip.shift && clip.shift > 0 && clip.shift !== 1 ? resample(s, clip.shift) : s.slice();
  for (let i = 0; i < out.length; i++) {
    if (clip.ring && clip.ring > 0) out[i] *= Math.fround(1.5 * Math.sin((2 * Math.PI * clip.ring * i) / sampleRate));
    if (clip.crush && clip.crush > 0 && clip.crush < 16) {
      const levels = 1 << (clip.crush - 1);
      out[i] = (Math.sign(out[i]) * Math.floor(Math.abs(out[i] * levels) + 0.5)) / levels;
    }
    if (clip.decay && clip.decay > 0) out[i] *= Math.fround(Math.exp(-i / ((clip.decay / 1000) * sampleRate)));
  }
  return out;
}

// Decodes 8 or 16-bit PCM WAV to mono samples at sampleRate. Chunks are walked by size, with
// the pad byte after odd-sized chunks, and anything other than fmt and data is skipped.
export function decodeWAV(b: Buffer): Float32Array {
  if (b.length < 12 || b.subarray(0, 4).toString() !== 'RIFF' || b.subarray(8, 12).toString() !== 'WAVE')
    throw new Error('not a WAV file');
  let channels = 0;
  let bits = 0;
  let rate = 0;
  let data: Buffer | undefined;
  let haveFmt = false;

  for (let off = 12; b.length - off >= 8;) {
    const id = b.subarray(off, off + 4).toString();
    const size = b.readUInt32LE(off + 4);
    if (size > b.length - off - 8) throw new Error('truncated WAV chunk');
    const body = b.subarray(off + 8, off + 8 + size);
    // The fmt chunk holds the format tag at 0 (1 is PCM), channels at 2, sample rate at 4 and
    // bits per sample at 14.
    if (id === 'fmt ') {
      if (body.length < 16) throw new Error('short fmt chunk');
      if (body.readUInt16LE(0) !== 1) throw new Error(`unsupported WAV encoding ${body.readUInt16LE(0)} (want PCM)`);
      channels = body.readUInt16LE(2);
      rate = body.readUInt32LE(4);
      bits = body.readUInt16LE(14);
      haveFmt = true;
    } else if (id === 'data') data = body;
    off += 8 + size;
    if (size % 2 && off < b.length) off++;
  }

  if (!haveFmt || !data) throw new Error('missing fmt or data chunk');
  if (channels < 1 || rate < 1 || ![8, 16].includes(bits))
    throw new Error(`unsupported WAV: ${channels} channels, ${rate} Hz, ${bits} bit`);
  const frame = (channels * bits) / 8;
  if (data.length % frame) throw new Error('incomplete WAV frame');

  // Average the channels. 8-bit PCM is unsigned around 128, 16-bit is signed.
  const mono = new Float32Array(data.length / frame);
  for (let i = 0; i < mono.length; i++) {
    let sum = 0;
    for (let c = 0; c < channels; c++) {
      const off = i * frame + (c * bits) / 8;
      sum = Math.fround(sum + (bits === 8 ? (data[off] - 128) / 128 : data.readInt16LE(off) / 32768));
    }
    mono[i] = sum / channels;
  }
  return rate === sampleRate ? mono : resample(mono, rate / sampleRate);
}

// Encodes mono 16-bit PCM at sampleRate behind a 44-byte header, clipping to full scale.
export function encodeWAV(samples: Float32Array, volume = 1): Buffer {
  const b = Buffer.alloc(44 + samples.length * 2);
  b.write('RIFF', 0);
  b.writeUInt32LE(b.length - 8, 4);
  b.write('WAVEfmt ', 8);
  b.writeUInt32LE(16, 16);
  b.writeUInt16LE(1, 20);
  b.writeUInt16LE(1, 22);
  b.writeUInt32LE(sampleRate, 24);
  b.writeUInt32LE(sampleRate * 2, 28);
  b.writeUInt16LE(2, 32);
  b.writeUInt16LE(16, 34);
  b.write('data', 36);
  b.writeUInt32LE(samples.length * 2, 40);
  for (let i = 0; i < samples.length; i++)
    b.writeInt16LE(Math.round(Math.max(-1, Math.min(1, samples[i] * volume)) * 32767), 44 + i * 2);
  return b;
}

// Clips play at 0.36 of full scale times their volume. Each clip with jitter gets four variants, and at most eight
// sounds play at once, four of them the same clip.
const level = 0.36;
const variantCount = 4;
const voiceCount = 8;
const clipLimit = 4;

// Streams are written in 10 ms blocks and kept 50 ms ahead of playback, which leaves room for Windows' 15.6 ms timer
// ticks. After the last sound ends, 250 ms of silence follows, because a server that buffers before it starts would
// otherwise hold a short clip back until the next one.
const blockMs = 10;
const blockFrames = (sampleRate * blockMs) / 1000;
const leadMs = 50;
const tailMs = 250;

// A variant being mixed, and how many of its samples have played.
interface Voice {
  name: string;
  samples: Float32Array;
  at: number;
}

// Mixes playing variants into 16-bit little-endian blocks for the outputs that take a raw stream.
export class Mixer {
  private voices: Voice[] = [];

  get active(): boolean {
    return this.voices.length > 0;
  }

  // Starts a variant, unless its clip is already playing four times. With every voice busy, the voice nearest its
  // end fades out over 3 ms instead of stopping with a click.
  add(name: string, samples: Float32Array): boolean {
    if (this.voices.filter(v => v.name === name).length >= clipLimit) return false;
    if (this.voices.length >= voiceCount) {
      const left = (v: Voice) => v.samples.length - v.at;
      const v = this.voices.reduce((a, b) => (left(b) < left(a) ? b : a));
      const fade = v.samples.slice(v.at, v.at + Math.trunc(0.003 * sampleRate));
      for (let i = 0; i < fade.length; i++) fade[i] *= 1 - i / fade.length;
      Object.assign(v, { name: '', samples: fade, at: 0 });
    }
    this.voices.push({ name, samples, at: 0 });
    return true;
  }

  // Sums the next frames of every voice, clipped to full scale, and drops the voices that finish.
  block(frames: number): Buffer {
    const out = Buffer.alloc(frames * 2);
    for (let i = 0; i < frames; i++) {
      let sum = 0;
      for (const v of this.voices) if (v.at + i < v.samples.length) sum += v.samples[v.at + i];
      out.writeInt16LE(Math.round(Math.max(-1, Math.min(1, sum)) * 32767), i * 2);
    }
    for (const v of this.voices) v.at += frames;
    this.voices = this.voices.filter(v => v.at < v.samples.length);
    return out;
  }
}

// A long-running raw output. queued() is how many milliseconds were written and not yet played.
export interface Stream {
  queued(): number;
  write(block: Buffer): void;
  close(): Promise<void>;
}

// pacat comes with paplay and works with PulseAudio and PipeWire. It asks for 30 ms of latency.
const pacatArgs = [
  '--playback',
  '--raw',
  '--format=s16le',
  `--rate=${sampleRate}`,
  '--channels=1',
  '--latency-msec=30',
  '--client-name=frost',
];

// Ends a child's input so it can stop by itself, and kills it if it's still running a second later.
async function endProcess(child: HelperProcess, exited: Promise<void>): Promise<void> {
  child.stdin.end();
  let timer: NodeJS.Timeout | undefined;
  const late = new Promise<boolean>(resolve => (timer = setTimeout(() => resolve(true), 1000)));
  if (await Promise.race([exited.then(() => false), late])) child.kill();
  clearTimeout(timer);
}

// Resolves when a child exits or fails to start.
const exitOf = (child: HelperProcess) =>
  new Promise<void>(resolve => {
    child.on('exit', resolve);
    child.on('error', () => resolve());
  });

// The macOS helper, run with osascript. It reads one JSON array per line. ['load', id, base64] keeps a variant's
// 32-bit float samples as buffer id. ['play', voice, id, steal] schedules that buffer on a voice, stopping the voice
// first when steal is 1. It prints 'ready' once its engine runs, or 'error' and a reason, and stops at end of input.
// A new output device stops the engine, so a play rebuilds it. Completion handlers are left nil through
// performSelector, because JavaScript can't run on the audio thread. The input node is never touched, so the
// microphone stays off.
const helperScript = `
ObjC.import('AVFoundation');
const output = $.NSFileHandle.fileHandleWithStandardOutput;
const say = text => output.writeData($(text + '\\n').dataUsingEncoding($.NSUTF8StringEncoding));
const format = $.AVAudioFormat.alloc.initStandardFormatWithSampleRateChannels(${sampleRate}, 1);
const buffers = [];
let engine;
let voices = [];

function start() {
  engine = $.AVAudioEngine.alloc.init;
  voices = [];
  for (let i = 0; i < ${voiceCount}; i++) {
    const voice = $.AVAudioPlayerNode.alloc.init;
    engine.attachNode(voice);
    engine.connectToFormat(voice, engine.mainMixerNode, format);
    voices.push(voice);
  }
  if (engine.startAndReturnError(null) !== true) return false;
  for (const voice of voices) voice.play;
  return true;
}

function handle(line) {
  const [command, a, b, c] = JSON.parse(line);
  if (command === 'load') {
    const bytes = $.NSData.alloc.initWithBase64EncodedStringOptions(b, 0);
    const frames = Number(bytes.length) / 4;
    if (frames < 1) return;
    const buffer = $.AVAudioPCMBuffer.alloc.initWithPCMFormatFrameCapacity(format, frames);
    buffer.frameLength = frames;
    bytes.getBytesLength(buffer.floatChannelData[0], frames * 4);
    buffers[a] = buffer;
  } else if (command === 'play' && buffers[b] && voices[a]) {
    if (engine.running !== true && !start()) return say('error the audio output stopped');
    const voice = voices[a];
    if (c) voice.stop;
    voice.performSelectorWithObjectWithObject('scheduleBuffer:completionHandler:', buffers[b], $());
    if (c) voice.play;
  }
}

if (!start()) say('error the audio output did not start');
else {
  say('ready');
  const input = $.NSFileHandle.fileHandleWithStandardInput;
  let pending = '';
  for (;;) {
    const data = input.availableData;
    if (Number(data.length) === 0) break;
    pending += $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding).js;
    for (let end = pending.indexOf('\\n'); end >= 0; end = pending.indexOf('\\n')) {
      try {
        handle(pending.slice(0, end));
      } catch (e) {
        say('error ' + String(e).replace(/\\s+/g, ' '));
      }
      pending = pending.slice(end + 1);
    }
  }
  engine.stop;
}
`;

// The parts of a child process the macOS helper needs. Tests pass a fake.
export interface HelperProcess {
  stdin: Writable;
  stdout: Readable;
  kill(): boolean;
  on(event: 'exit', listener: () => void): unknown;
  on(event: 'error', listener: (e: Error) => void): unknown;
}

const spawnHelper = (program: string, args: string[]): HelperProcess =>
  spawn(program, args, { stdio: ['pipe', 'pipe', 'ignore'] });

interface SoundOptions {
  env?: NodeJS.ProcessEnv;
  platform?: NodeJS.Platform;
  runner?: Runner;
  spawn?: (program: string, args: string[]) => HelperProcess;
  openStream?: () => Promise<Stream | undefined>;
  cacheDir?: string;
  random?: () => number;
  now?: () => number;
}

// A shaped clip and its variants. ids are the helper's buffer numbers and files the variants written for the
// platform's player. last is the variant played last, which the next play avoids.
interface Sound {
  clip: Clip;
  samples: Float32Array;
  variants: Float32Array[];
  ids: number[];
  files: string[];
  last: number;
}

// Plays named clips. Setup and playback failures don't throw; err() reports the last one.
export class Player {
  private readonly sounds = new Map<string, Sound>();
  private readonly platform: NodeJS.Platform;
  private readonly runner: Runner;
  private readonly random: () => number;
  private readonly now: () => number;
  private ready = false;
  private error?: Error;
  private closed = false;
  private closing?: Promise<void>;
  private readonly tasks = new Set<Promise<void>>();
  private readonly abort = new AbortController();

  // The helper and when each of its voices falls silent, in now() milliseconds.
  private helper?: HelperProcess;
  private helperExit?: Promise<void>;
  private readonly voices = Array.from({ length: voiceCount }, () => ({ name: '', until: -Infinity }));

  // The stream, its mixer, the silence still to write after the last sound, and the timer that keeps it fed.
  private stream?: Stream;
  private readonly mixer = new Mixer();
  private tail = 0;
  private timer?: NodeJS.Timeout;

  // The private folder for the platform's player, and how many of its plays are running for each clip.
  private directory?: string;
  private readonly running = new Map<string, number>();
  private runningTotal = 0;

  // Clips are decoded and shaped up front. Variants render and the output starts in the background, and play()
  // does nothing until the output is ready.
  constructor(
    clips: Record<string, Clip>,
    private readonly options: SoundOptions = {},
  ) {
    this.platform = options.platform ?? process.platform;
    this.runner = options.runner ?? run;
    this.random = options.random ?? Math.random;
    this.now = options.now ?? (() => performance.now());
    if ((options.env ?? process.env).FROST_NO_SOUND) {
      this.error = new Error('sound disabled by FROST_NO_SOUND');
      return;
    }
    try {
      for (const [name, clip] of Object.entries(clips))
        this.sounds.set(name, {
          clip,
          samples: cut(effects(decodeWAV(clip.wav), clip), clip.cut),
          variants: [],
          ids: [],
          files: [],
          last: -1,
        });
    } catch (e) {
      this.error = e as Error;
      return;
    }
    this.track(this.init());
  }

  private track(task: Promise<void>): void {
    this.tasks.add(task);
    void task.finally(() => this.tasks.delete(task));
  }

  // The macOS helper and the streams take a moment to start, so they start while the variants render.
  private async init(): Promise<void> {
    try {
      const helper = this.platform === 'darwin' ? this.startHelper() : undefined;
      const stream =
        this.platform === 'darwin'
          ? undefined
          : this.openStream().catch(e => {
              this.error = e as Error;
              return undefined;
            });
      await this.render();
      if (helper) {
        this.loadHelper();
        if (await helper) {
          this.ready = !this.closed;
          return;
        }
        await this.stopHelper();
      }
      const opened = await stream;
      if (opened && this.closed) await opened.close();
      else if (opened) {
        this.stream = opened;
        this.ready = true;
        return;
      }
      if (!this.closed) await this.useFiles();
    } catch (e) {
      if (!this.closed) this.error = e as Error;
    }
  }

  // Renders every variant at playback level, clipped to full scale. Clips without jitter get one. Each variant
  // takes a few milliseconds, so the event loop gets a turn between them.
  private async render(): Promise<void> {
    for (const sound of this.sounds.values()) {
      const count = sound.clip.pitch || sound.clip.tempo ? variantCount : 1;
      const gain = level * (sound.clip.volume || 1);
      for (let i = 0; i < count && !this.closed; i++) {
        const v = vary(sound.samples, jitter(sound.clip.pitch, this.random), jitter(sound.clip.tempo, this.random));
        sound.variants.push(v.map(x => Math.max(-1, Math.min(1, x * gain))));
        await new Promise(resolve => setImmediate(resolve));
      }
    }
  }

  // Starts the macOS helper. The promise resolves true once its engine runs, and false if it can't start, reports
  // an error, exits or stays silent for 10 seconds. A helper that stops later turns sound off.
  private startHelper(): Promise<boolean> {
    let child: HelperProcess;
    try {
      child = (this.options.spawn ?? spawnHelper)('osascript', ['-l', 'JavaScript', '-e', helperScript]);
    } catch (e) {
      this.error = e as Error;
      return Promise.resolve(false);
    }
    this.helper = child;
    child.stdin.on('error', () => {});
    let exited!: () => void;
    this.helperExit = new Promise(resolve => (exited = resolve));
    return new Promise(resolve => {
      const done = (ok: boolean) => {
        clearTimeout(timer);
        resolve(ok);
      };
      const timer = setTimeout(() => done(false), 10_000);
      let lines = '';
      child.stdout.on('data', (data: Buffer) => {
        lines += data;
        for (let end = lines.indexOf('\n'); end >= 0; end = lines.indexOf('\n')) {
          const line = lines.slice(0, end).trim();
          lines = lines.slice(end + 1);
          if (line === 'ready') done(true);
          else if (line.startsWith('error ')) {
            this.error = new Error(line.slice(6));
            done(false);
          }
        }
      });
      const stopped = () => {
        exited();
        if (this.helper === child) {
          this.ready = false;
          this.error ??= new Error('the audio helper stopped');
        }
        done(false);
      };
      child.on('error', e => {
        if (this.helper === child) this.error = e;
        stopped();
      });
      child.on('exit', stopped);
    });
  }

  // Sends every variant to the helper, which reads them after its engine starts.
  private loadHelper(): void {
    let id = 0;
    for (const sound of this.sounds.values())
      for (const v of sound.variants) {
        sound.ids.push(id);
        this.send(['load', id++, Buffer.from(v.buffer, v.byteOffset, v.byteLength).toString('base64')]);
      }
  }

  private send(message: unknown[]): void {
    this.helper?.stdin.write(JSON.stringify(message) + '\n');
  }

  // The helper stops its engine and exits at the end of its input.
  private async stopHelper(): Promise<void> {
    const child = this.helper;
    if (!child) return;
    this.helper = undefined;
    await endProcess(child, this.helperExit!);
  }

  // Opens winmm's waveOut on Windows, or pacat elsewhere. Resolves undefined when the output can't start.
  private async openStream(): Promise<Stream | undefined> {
    if (this.options.openStream) return this.options.openStream();
    if (this.platform === 'win32') {
      try {
        const { WaveOut } = await import('./waveout.js');
        const device = new WaveOut(blockFrames * 2);
        return {
          queued: () => device.pending() * blockMs,
          write: block => device.write(block),
          close: async () => device.close(),
        };
      } catch (e) {
        this.error = e as Error;
        return undefined;
      }
    }

    // pacat says nothing reliable once it's connected, so one still running after 250 ms counts as working. Queued
    // time is what was written minus the time since writing started, which restarts whenever the queue drains.
    let child: HelperProcess;
    try {
      child = (this.options.spawn ?? spawnHelper)('pacat', pacatArgs);
    } catch (e) {
      this.error = e as Error;
      return undefined;
    }
    child.stdin.on('error', () => {});
    child.stdout.resume();
    let running = true;
    const exited = exitOf(child).then(() => {
      running = false;
    });
    let timer: NodeJS.Timeout | undefined;
    await Promise.race([exited, new Promise(resolve => (timer = setTimeout(resolve, 250)))]);
    clearTimeout(timer);
    if (!running) {
      this.error = new Error('pacat could not play sound');
      return undefined;
    }
    let start = 0;
    let written = 0;
    const stream: Stream = {
      queued: () => {
        const left = written - (this.now() - start);
        if (left > 0) return left;
        start = this.now();
        written = 0;
        return 0;
      },
      write: block => {
        if (!running) throw new Error('pacat stopped');
        child.stdin.write(block);
        written += (block.length / 2 / sampleRate) * 1000;
      },
      close: () => endProcess(child, exited),
    };
    void exited.then(() => this.streamStopped(stream));
    return stream;
  }

  // A stream that stops or fails while in use hands over to the platform's player.
  private streamStopped(stream: Stream, e?: unknown): void {
    if (this.stream !== stream) return;
    this.stream = undefined;
    this.ready = false;
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    this.error = e instanceof Error ? e : new Error('the audio stream stopped');
    void stream.close().catch(() => {});
    if (!this.closed)
      this.track(
        this.useFiles().catch(e => {
          if (!this.closed) this.error = e as Error;
        }),
      );
  }

  // Writes each variant to a private folder once and checks the platform's player runs. On macOS any exit code
  // passes, so only a missing afplay fails.
  private async useFiles(): Promise<void> {
    const base = this.options.cacheDir ?? os.tmpdir();
    await mkdir(base, { recursive: true, mode: 0o700 });
    this.directory = await mkdtemp(path.join(base, 'frost-audio-'));
    let n = 0;
    for (const sound of this.sounds.values())
      for (const v of sound.variants) {
        const file = path.join(this.directory, `${n++}.wav`);
        await writeFile(file, encodeWAV(v), { mode: 0o600, flag: 'wx' });
        sound.files.push(file);
      }
    const probe =
      this.platform === 'win32'
        ? ([
            'powershell.exe',
            ['-NoProfile', '-NonInteractive', '-Command', '[void][System.Media.SoundPlayer]'],
          ] as const)
        : this.platform === 'darwin'
          ? (['afplay', ['-h']] as const)
          : (['paplay', ['--version']] as const);
    const result = await this.runner(probe[0], [...probe[1]], undefined, this.abort.signal);
    if (result.code && this.platform !== 'darwin') throw new Error('no audio device');
    this.ready = !this.closed;
  }

  available(): boolean {
    return this.ready;
  }

  err(): Error | undefined {
    return this.error;
  }

  // Fire and forget. Picks a variant other than the last one, then plays it on the helper or the platform's player.
  play(name: string): void {
    const sound = this.sounds.get(name);
    if (!sound || !this.ready || this.closed) return;
    let variant = Math.floor(this.random() * sound.variants.length);
    if (variant === sound.last && sound.variants.length > 1) variant = (variant + 1) % sound.variants.length;
    const played = this.helper
      ? this.playHelper(name, sound, variant)
      : this.stream
        ? this.playStream(name, sound.variants[variant])
        : this.playFile(name, sound.files[variant]);
    if (played) sound.last = variant;
  }

  private playStream(name: string, samples: Float32Array): boolean {
    if (!this.mixer.add(name, samples)) return false;
    this.tail = tailMs;
    this.pump();
    return true;
  }

  // Writes blocks until the stream is far enough ahead. A new sound goes out straight away, and a timer keeps the
  // stream fed only while there's sound or the silent tail left to write.
  private pump(): void {
    const stream = this.stream;
    if (!stream) return;
    try {
      while ((this.mixer.active || this.tail > 0) && stream.queued() < leadMs) {
        if (!this.mixer.active) this.tail -= blockMs;
        stream.write(this.mixer.block(blockFrames));
      }
    } catch (e) {
      this.streamStopped(stream, e);
      return;
    }
    const busy = this.mixer.active || this.tail > 0;
    if (busy && !this.timer) {
      this.timer = setInterval(() => this.pump(), blockMs);
      this.timer.unref();
    } else if (!busy && this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
  }

  // Takes the voice that falls silent first, which is an idle one when any are free, and steals it otherwise.
  // Voices count as busy for 50 ms past the clip, in case it started late.
  private playHelper(name: string, sound: Sound, variant: number): boolean {
    const now = this.now();
    let pick = 0;
    let playing = 0;
    for (let i = 0; i < this.voices.length; i++) {
      if (this.voices[i].name === name && this.voices[i].until > now) playing++;
      if (this.voices[i].until < this.voices[pick].until) pick = i;
    }
    if (playing >= clipLimit) return false;
    const steal = this.voices[pick].until > now;
    this.voices[pick] = { name, until: now + (sound.variants[variant].length / sampleRate) * 1000 + 50 };
    this.send(['play', pick, sound.ids[variant], steal ? 1 : 0]);
    return true;
  }

  // Runs the platform's player on a variant's file. The Windows path travels base64-encoded so PowerShell needs no
  // quoting.
  private playFile(name: string, file: string): boolean {
    const count = this.running.get(name) ?? 0;
    if (count >= clipLimit || this.runningTotal >= voiceCount) return false;
    this.running.set(name, count + 1);
    this.runningTotal++;
    let program = this.platform === 'darwin' ? 'afplay' : 'paplay';
    let args = [file];
    if (this.platform === 'win32') {
      const ps =
        '$p=New-Object System.Media.SoundPlayer; $p.SoundLocation=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("' +
        Buffer.from(file).toString('base64') +
        '")); $p.PlaySync(); $p.Dispose()';
      program = 'powershell.exe';
      args = ['-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(ps, 'utf16le').toString('base64')];
    }
    this.track(
      this.runner(program, args, undefined, this.abort.signal)
        .then(
          () => {},
          e => {
            if (!this.closed) this.error = e;
          },
        )
        .finally(() => {
          this.running.set(name, (this.running.get(name) ?? 1) - 1);
          this.runningTotal--;
        }),
    );
    return true;
  }

  // Stops new plays, stops the helper, the stream or any running players, and removes the private folder.
  close(): Promise<void> {
    return (this.closing ??= (async () => {
      this.closed = true;
      this.ready = false;
      if (this.timer) clearInterval(this.timer);
      this.timer = undefined;
      this.abort.abort();
      await this.stopHelper();
      const stream = this.stream;
      this.stream = undefined;
      await stream?.close().catch(() => {});
      await Promise.allSettled(this.tasks);
      if (this.directory) await rm(this.directory, { recursive: true, force: true });
    })());
  }
}
