// Sound effects for the TUI. Clips are decoded and shaped once, varied slightly on each play,
// written as WAV files to a private temporary folder and played with the platform's player.
// FROST_NO_SOUND turns it all off.
import { mkdir, mkdtemp, writeFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
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

interface SoundOptions {
  env?: NodeJS.ProcessEnv;
  platform?: NodeJS.Platform;
  runner?: Runner;
  cacheDir?: string;
}

// Plays named clips. Setup and playback failures don't throw; err() reports the last one.
export class Player {
  private readonly clips = new Map<string, { samples: Float32Array; clip: Clip }>();
  private readonly active = new Map<string, number>();
  private ready = false;
  private error?: Error;
  private directory?: string;
  private closed = false;
  private tasks = new Set<Promise<void>>();

  // Clips are decoded and shaped up front. The player check runs in the background, and play()
  // does nothing until it passes.
  constructor(
    clips: Record<string, Clip>,
    private readonly options: SoundOptions = {},
  ) {
    if ((options.env ?? process.env).FROST_NO_SOUND) {
      this.error = new Error('sound disabled by FROST_NO_SOUND');
      return;
    }
    try {
      for (const [name, clip] of Object.entries(clips))
        this.clips.set(name, { samples: cut(effects(decodeWAV(clip.wav), clip), clip.cut), clip });
    } catch (e) {
      this.error = e as Error;
      return;
    }
    const init = this.init();
    this.tasks.add(init);
    void init.finally(() => this.tasks.delete(init));
  }

  // Creates the private folder and checks the player runs. On macOS any exit code passes, so
  // only a missing afplay fails. A close() during the check keeps the player off.
  private async init(): Promise<void> {
    try {
      const platform = this.options.platform ?? process.platform;
      const runner = this.options.runner ?? run;
      const base = this.options.cacheDir ?? os.tmpdir();
      await mkdir(base, { recursive: true, mode: 0o700 });
      this.directory = await mkdtemp(path.join(base, 'frost-audio-'));
      const probe =
        platform === 'win32'
          ? ([
              'powershell.exe',
              ['-NoProfile', '-NonInteractive', '-Command', '[void][System.Media.SoundPlayer]'],
            ] as const)
          : platform === 'darwin'
            ? (['afplay', ['-h']] as const)
            : (['paplay', ['--version']] as const);
      const result = await runner(probe[0], [...probe[1]]);
      if (result.code && platform !== 'darwin') throw new Error('no audio device');
      this.ready = !this.closed;
    } catch (e) {
      this.error = e as Error;
    }
  }

  available(): boolean {
    return this.ready;
  }

  err(): Error | undefined {
    return this.error;
  }

  // Fire and forget, with up to 4 overlapping plays of the same clip.
  play(name: string): void {
    const c = this.clips.get(name);
    const count = this.active.get(name) ?? 0;
    if (!c || !this.ready || this.closed || count >= 4) return;
    this.active.set(name, count + 1);
    const task = this.playClip(name, c.samples, c.clip)
      .catch(e => {
        this.error = e;
      })
      .finally(() => {
        this.active.set(name, (this.active.get(name) ?? 1) - 1);
        this.tasks.delete(task);
      });
    this.tasks.add(task);
  }

  // Each play gets fresh pitch and tempo jitter and its own file, deleted afterwards. The
  // Windows path travels base64-encoded so PowerShell needs no quoting.
  private async playClip(name: string, samples: Float32Array, clip: Clip): Promise<void> {
    const file = path.join(this.directory!, randomUUID() + '.wav');
    try {
      await writeFile(
        file,
        encodeWAV(vary(samples, jitter(clip.pitch), jitter(clip.tempo)), 0.36 * (clip.volume || 1)),
        { mode: 0o600, flag: 'wx' },
      );
      const platform = this.options.platform ?? process.platform;
      const runner = this.options.runner ?? run;
      if (platform === 'win32') {
        const ps =
          '$p=New-Object System.Media.SoundPlayer; $p.SoundLocation=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("' +
          Buffer.from(file).toString('base64') +
          '")); $p.PlaySync(); $p.Dispose()';
        await runner('powershell.exe', [
          '-NoProfile',
          '-NonInteractive',
          '-EncodedCommand',
          Buffer.from(ps, 'utf16le').toString('base64'),
        ]);
      } else await runner(platform === 'darwin' ? 'afplay' : 'paplay', [file]);
    } finally {
      await rm(file, { force: true });
    }
  }

  // Stops new plays, waits for the running ones and removes the folder.
  async close(): Promise<void> {
    this.closed = true;
    this.ready = false;
    await Promise.allSettled(this.tasks);
    if (this.directory) await rm(this.directory, { recursive: true, force: true });
  }
}
