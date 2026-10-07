// Tests for desktop actions (opening URLs, revealing files, picking folders) with stubbed command
// runners, and for the sound effects and WAV codec. Nothing opens a browser or plays audio.

import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { PassThrough } from 'node:stream';
import { available, openCommand, revealCommand, openBrowser, pickFolder } from '../../src/platform/desktop.js';
import {
  Player,
  Mixer,
  decodeWAV,
  encodeWAV,
  resample,
  vary,
  cut,
  effects,
  sampleRate,
  type HelperProcess,
  type Stream,
} from '../../src/platform/sound.js';
import { WaveOut, type WinMM } from '../../src/platform/waveout.js';

// An SSH session has no usable desktop even when DISPLAY is set. URLs and paths are passed as
// single arguments, never through a shell.
test('desktop integrations preserve URLs and treat paths as arguments', async () => {
  assert.equal(available({ DISPLAY: ':0' }, 'linux'), true);
  assert.equal(available({ SSH_TTY: '/dev/tty', DISPLAY: ':0' }, 'linux'), false);
  assert.deepEqual(openCommand('https://example.com/?a=1&b=2', 'win32'), [
    'rundll32',
    ['url.dll,FileProtocolHandler', 'https://example.com/?a=1&b=2'],
  ]);
  assert.deepEqual(revealCommand('/a/file', 'darwin'), ['open', ['-R', '/a/file']]);
  assert.deepEqual(revealCommand('C:\\my files\\notes.txt', 'win32'), [
    'explorer',
    ['/select,"C:\\my files\\notes.txt"'],
  ]);

  let calls = 0;
  await openBrowser('https://example.com/', {
    runner: async () => {
      calls++;
      return { code: 0, stdout: '', stderr: '' };
    },
  });
  assert.equal(calls, 1);

  // A cancelled picker exits non-zero.
  await assert.rejects(
    pickFolder('title', '/start', { platform: 'darwin', runner: async () => ({ code: 1, stdout: '', stderr: '' }) }),
    /no folder chosen/,
  );

  // On macOS, choose folder fails without opening when its start folder is missing, so only a folder
  // that exists is passed.
  const scripts: string[] = [];
  const picker = async (_: string, args: string[]) => {
    scripts.push(args[1]);
    return { code: 0, stdout: '/Users/me/Picked/\n', stderr: '' };
  };
  const here = process.cwd();
  assert.equal(await pickFolder('Restore "to"', here, { platform: 'darwin', runner: picker }), '/Users/me/Picked/');
  await pickFolder('Restore to', path.join(here, 'no such folder'), { platform: 'darwin', runner: picker });
  assert.deepEqual(scripts, [
    `POSIX path of (choose folder with prompt "Restore \\"to\\"" default location (POSIX file ${JSON.stringify(here)}))`,
    'POSIX path of (choose folder with prompt "Restore to")',
  ]);
});

const sine = (hz: number, n: number) =>
  Float32Array.from({ length: n }, (_, i) => 0.8 * Math.sin((2 * Math.PI * hz * i) / sampleRate));

// Estimates frequency by counting upward zero crossings in the middle three fifths, away from
// any fade at either end.
function pitch(samples: Float32Array): number {
  const start = Math.trunc(samples.length / 5);
  const end = Math.trunc((samples.length * 4) / 5);
  let n = 0;
  for (let i = start + 1; i < end; i++) if (samples[i - 1] < 0 && samples[i] >= 0) n++;
  return (n * sampleRate) / (end - start);
}

test('sound effects preserve independent pitch and tempo, fading, and input', () => {
  const samples = sine(440, 22050);
  assert.deepEqual(vary(samples, 1, 1), samples);

  // Raising pitch 5% keeps the length. Speeding up 5% keeps the pitch.
  const shifted = vary(samples, 1.05, 1);
  const faster = vary(samples, 1, 1.05);
  assert.ok(Math.abs(shifted.length - samples.length) <= 1);
  assert.ok(Math.abs(pitch(shifted) - 462) < 8);
  assert.ok(Math.abs(faster.length - samples.length / 1.05) < 2);
  assert.ok(Math.abs(pitch(faster) - 440) < 8);

  // Cutting to 150 ms at 44.1 kHz keeps 6615 samples, fades the tail and leaves the source alone.
  const source = Float32Array.from({ length: 44100 }, () => 1);
  const shortened = cut(source, 150);
  assert.equal(shortened.length, 6615);
  assert.ok(shortened.at(-1)! < 0.01);
  assert.equal(source[6614], 1);
  assert.equal(resample(samples, 2).length, Math.trunc(samples.length / 2));

  const processed = effects(samples, { wav: Buffer.alloc(0), shift: 2, ring: 400, crush: 4, decay: 100 });
  assert.ok(processed.length < samples.length);
  assert.equal(samples[0], 0);
});

test('WAV decoder validates structure and preserves PCM', async () => {
  const samples = sine(330, 4000);
  const wav = encodeWAV(samples);
  const restored = decodeWAV(wav);
  assert.equal(restored.length, samples.length);
  assert.ok(Math.abs(restored[100] - samples[100]) < 0.0001);
  for (const bytes of [Buffer.alloc(0), Buffer.from('RIFFxxxxWAVE'), wav.subarray(0, 30), Buffer.from('BAD!')])
    assert.throws(() => decodeWAV(bytes));

  // FROST_NO_SOUND turns the player off before it ever runs an audio command.
  const player = new Player(
    { tone: { wav } },
    {
      env: { FROST_NO_SOUND: '1' },
      runner: async () => {
        throw new Error('must not open audio');
      },
    },
  );
  assert.equal(player.available(), false);
  player.play('tone');
  assert.match(player.err()!.message, /disabled/);
  await player.close();
});

// A stand-in for a child process. It records what frost writes and exits when its input ends, unless it's stuck,
// when only kill() stops it.
function fakeProcess(stuck = false) {
  const stdin = new PassThrough();
  const stdout = new PassThrough();
  const chunks: Buffer[] = [];
  stdin.on('data', data => chunks.push(data));
  const child = Object.assign(new EventEmitter(), {
    stdin,
    stdout,
    killed: false,
    kill() {
      child.killed = true;
      child.emit('exit');
      return true;
    },
  });
  if (!stuck) stdin.on('end', () => child.emit('exit'));
  return { child: child as HelperProcess & typeof child, chunks };
}

// The macOS helper, recording each message frost sends.
function fakeHelper(stuck = false) {
  const { child } = fakeProcess(stuck);
  const sent: unknown[][] = [];
  let text = '';
  child.stdin.on('data', data => {
    text += data;
    for (let end = text.indexOf('\n'); end >= 0; end = text.indexOf('\n')) {
      sent.push(JSON.parse(text.slice(0, end)));
      text = text.slice(end + 1);
    }
  });
  return { child, sent };
}

// Waits for background work without timers, so mocked timers can't stall it.
async function until(done: () => boolean): Promise<void> {
  for (let i = 0; i < 10000 && !done(); i++) await new Promise(resolve => setImmediate(resolve));
  assert.ok(done(), 'timed out');
}

const floats = (base64: string) => {
  const b = Buffer.from(base64, 'base64');
  return new Float32Array(b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength));
};

// A 100 ms tone. With random fixed at 0 there's no jitter, so every variant keeps its length.
const tone = encodeWAV(sine(440, 4410));

test('the macOS helper loads variants once, ignores early plays and shares its voices', async () => {
  const { child, sent } = fakeHelper();
  const spawned: string[][] = [];
  let clock = 0;
  const player = new Player(
    { a: { wav: tone, pitch: 0.05, volume: 0.5 }, b: { wav: tone, pitch: 0.05 }, c: { wav: tone } },
    {
      env: {},
      platform: 'darwin',
      random: () => 0,
      now: () => clock,
      spawn: (program, args) => {
        spawned.push([program, ...args]);
        return child;
      },
      runner: async () => {
        throw new Error('must not run a player');
      },
    },
  );

  // Clips with jitter get four variants and the plain one gets one. They arrive as float samples at playback level,
  // and a play before the helper is ready is dropped rather than queued.
  player.play('a');
  await until(() => sent.length === 9);
  assert.deepEqual(spawned[0].slice(0, 4), ['osascript', '-l', 'JavaScript', '-e']);
  assert.deepEqual(
    sent.map(([command, id]) => [command, id]),
    Array.from({ length: 9 }, (_, i) => ['load', i]),
  );
  const plain = floats(sent[8][2] as string);
  assert.equal(plain.length, 4410);
  assert.ok(Math.abs(Math.max(...plain) - 0.8 * 0.36) < 0.001);
  assert.ok(Math.abs(Math.max(...floats(sent[0][2] as string)) - 0.8 * 0.36 * 0.5) < 0.001);
  assert.equal(player.available(), false);

  child.stdout.write('rea');
  child.stdout.write('dy\n');
  await until(() => player.available());

  // Each play takes an idle voice and avoids repeating the last variant. A fifth copy of a playing clip is dropped,
  // and with every voice busy the one that ends first is stolen.
  for (const name of ['a', 'a', 'a', 'a', 'a', 'b', 'b', 'b', 'b', 'c']) {
    player.play(name);
    clock++;
  }
  clock = 1000;
  player.play('c');
  await until(() => sent.length === 9 + 10);
  assert.deepEqual(sent.slice(9), [
    ['play', 0, 0, 0],
    ['play', 1, 1, 0],
    ['play', 2, 0, 0],
    ['play', 3, 1, 0],
    ['play', 4, 4, 0],
    ['play', 5, 5, 0],
    ['play', 6, 4, 0],
    ['play', 7, 5, 0],
    ['play', 0, 8, 1],
    ['play', 1, 8, 0],
  ]);

  // Closing ends the helper's input, and it exits by itself.
  await player.close();
  assert.equal(child.stdin.writableEnded, true);
  assert.equal(child.killed, false);
  player.play('c');
  assert.equal(sent.length, 19);
  assert.equal(player.err(), undefined);
});

test('a stuck macOS helper is killed, and one that stops turns sound off', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const stuck = fakeHelper(true);
  const player = new Player({ c: { wav: tone } }, { env: {}, platform: 'darwin', spawn: () => stuck.child });
  await until(() => stuck.sent.length === 1);
  stuck.child.stdout.write('ready\n');
  await until(() => player.available());
  const closing = player.close();
  await until(() => stuck.child.stdin.writableEnded);
  t.mock.timers.tick(1000);
  await closing;
  assert.equal(stuck.child.killed, true);

  const crashing = fakeHelper();
  const other = new Player({ c: { wav: tone } }, { env: {}, platform: 'darwin', spawn: () => crashing.child });
  await until(() => crashing.sent.length === 1);
  crashing.child.stdout.write('ready\n');
  await until(() => other.available());
  crashing.child.emit('exit');
  assert.equal(other.available(), false);
  assert.match(other.err()!.message, /helper stopped/);
  await other.close();
});

test('sound falls back to the platform player, reuses its files and stops them on close', async t => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'frost-sound-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const calls: string[][] = [];
  // Probes pass straight away. Plays run until close() aborts them.
  const runner = async (program: string, args: string[], _?: unknown, signal?: AbortSignal) => {
    calls.push([program, ...args]);
    if (args[0] === '-h' || args[0] === '--version') return { code: 0, stdout: '', stderr: '' };
    return new Promise<never>((_, reject) => signal!.addEventListener('abort', () => reject(new Error('aborted'))));
  };

  // On macOS a helper that can't start, like a missing osascript, leaves afplay.
  const player = new Player(
    { a: { wav: tone, pitch: 0.05 } },
    {
      env: {},
      platform: 'darwin',
      cacheDir: dir,
      random: () => 0,
      runner,
      spawn: () => {
        const { child } = fakeHelper();
        setImmediate(() => child.emit('error', new Error('spawn osascript ENOENT')));
        return child;
      },
    },
  );
  await until(() => player.available());
  assert.match(player.err()!.message, /ENOENT/);
  const [folder] = await readdir(dir);
  const files = (await readdir(path.join(dir, folder))).map(f => path.join(dir, folder, f)).sort();
  assert.equal(files.length, 4);
  assert.equal(decodeWAV(await readFile(files[0])).length, 4410);

  // Four copies of a clip play at once, from the files written up front, and closing stops them and the folder.
  for (let i = 0; i < 5; i++) player.play('a');
  assert.deepEqual(calls.slice(1), [
    ['afplay', files[0]],
    ['afplay', files[1]],
    ['afplay', files[0]],
    ['afplay', files[1]],
  ]);
  await player.close();
  assert.deepEqual(await readdir(dir), []);
  assert.equal(player.err()!.message, 'spawn osascript ENOENT');

  // On Linux a pacat that exits straight away leaves paplay, and at most eight sounds play at once.
  calls.length = 0;
  const spawned: string[] = [];
  const linux = new Player(
    { a: { wav: tone }, b: { wav: tone }, c: { wav: tone } },
    {
      env: {},
      platform: 'linux',
      cacheDir: dir,
      runner,
      spawn: program => {
        spawned.push(program);
        const { child } = fakeProcess();
        setImmediate(() => child.emit('exit'));
        return child;
      },
    },
  );
  await until(() => linux.available());
  assert.deepEqual(spawned, ['pacat']);
  assert.match(linux.err()!.message, /pacat could not play/);
  for (const name of ['a', 'a', 'a', 'a', 'b', 'b', 'b', 'b', 'c']) linux.play(name);
  assert.deepEqual(calls[0], ['paplay', '--version']);
  assert.equal(calls.length, 1 + 8);
  assert.ok(calls.slice(1).every(([program]) => program === 'paplay'));
  await linux.close();
  assert.deepEqual(await readdir(dir), []);
});

const int16 = (block: Buffer) => Array.from({ length: block.length / 2 }, (_, i) => block.readInt16LE(i * 2));

test('the mixer sums voices, clips to full scale, limits each clip and fades a stolen voice', () => {
  const mixer = new Mixer();
  const half = new Float32Array(100).fill(0.5);
  const loud = new Float32Array(50).fill(0.75);
  assert.equal(mixer.active, false);
  assert.ok(mixer.add('a', half));
  assert.ok(mixer.add('b', loud));
  const first = int16(mixer.block(60));
  assert.equal(first[0], 32767);
  assert.equal(first[55], Math.round(0.5 * 32767));
  assert.deepEqual(int16(mixer.block(60)).slice(40), Array(20).fill(0));
  assert.equal(mixer.active, false);

  // A fifth copy of a clip is refused. With eight voices busy, the one nearest its end fades out over 3 ms.
  for (let i = 0; i < 4; i++) assert.ok(mixer.add('a', new Float32Array(1000).fill(0.01)));
  assert.equal(mixer.add('a', half), false);
  for (let i = 0; i < 3; i++) mixer.add('b', new Float32Array(1000).fill(0.01));
  mixer.add('c', new Float32Array(500).fill(0.1));
  assert.ok(mixer.add('d', new Float32Array(1000).fill(0.01)));
  const mixed = int16(mixer.block(400));
  const fade = Math.trunc(0.003 * sampleRate);
  assert.ok(mixed[0] > mixed[fade - 1]);
  assert.equal(mixed[fade + 1], Math.round(0.08 * 32767));
});

test('a stream starts a sound straight away, adds a silent tail and stops writing when idle', async t => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const blocks: Buffer[] = [];
  let played = 0;
  let closed = 0;
  const stream: Stream = {
    queued: () => Math.max(0, blocks.length * 10 - played),
    write: block => blocks.push(block),
    close: async () => {
      closed++;
    },
  };
  const player = new Player({ a: { wav: tone } }, { env: {}, platform: 'linux', openStream: async () => stream });
  await until(() => player.available());

  // The first 50 ms go out during the play itself. The 100 ms tone is followed by 250 ms of silence, then nothing.
  player.play('a');
  assert.equal(blocks.length, 5);
  for (let i = 0; i < 60; i++) {
    played += 10;
    t.mock.timers.tick(10);
  }
  assert.equal(blocks.length, 35);
  assert.ok(blocks.every(b => b.length === 882));
  const samples = blocks.flatMap(int16);
  assert.equal(samples[100], Math.round(sine(440, 4410)[100] * 0.36 * 32767));
  assert.ok(samples.slice(4410).every(x => x === 0));

  await player.close();
  assert.equal(closed, 1);
  player.play('a');
  t.mock.timers.tick(100);
  assert.equal(blocks.length, 35);
});

test('pacat streams raw audio and hands over to paplay when it stops', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const dir = await mkdtemp(path.join(os.tmpdir(), 'frost-sound-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const { child, chunks } = fakeProcess();
  const spawned: string[][] = [];
  const calls: string[][] = [];
  const player = new Player(
    { a: { wav: tone } },
    {
      env: {},
      platform: 'linux',
      cacheDir: dir,
      now: () => 0,
      spawn: (program, args) => {
        spawned.push([program, ...args]);
        return child;
      },
      runner: async (program, args) => {
        calls.push([program, ...args]);
        return { code: 0, stdout: '', stderr: '' };
      },
    },
  );

  // A pacat still running after 250 ms counts as working.
  await until(() => spawned.length === 1);
  assert.deepEqual(spawned[0], [
    'pacat',
    '--playback',
    '--raw',
    '--format=s16le',
    '--rate=44100',
    '--channels=1',
    '--latency-msec=30',
    '--client-name=frost',
  ]);
  await new Promise(resolve => setImmediate(resolve));
  t.mock.timers.tick(250);
  await until(() => player.available());
  player.play('a');
  const written = Buffer.concat(chunks);
  assert.equal(written.length, 5 * 882);
  assert.equal(written.readInt16LE(200), Math.round(sine(440, 4410)[100] * 0.36 * 32767));
  assert.deepEqual(calls, []);

  // When pacat stops, sound carries on through paplay.
  child.emit('exit');
  await until(() => calls.length === 1 && player.available());
  assert.deepEqual(calls[0], ['paplay', '--version']);
  player.play('a');
  assert.equal(calls[1][0], 'paplay');
  assert.match(player.err()!.message, /stream stopped/);
  await player.close();
});

// A winmm stand-in. Prepare marks a header prepared, reset marks every header done, and pointer() hands out fake
// addresses so headers can be traced back to their blocks.
function fakeWinMM(failOpen = 0, failPrepare = -1) {
  const calls: string[] = [];
  const blocks = new Map<bigint, Buffer>();
  const headers: Buffer[] = [];
  let next = 0x10000n;
  const api: WinMM = {
    waveOutOpen: (handle, device, format, _callback, _instance, flags) => {
      calls.push(`open ${device.toString(16)} ${flags} ${format.toString('hex')}`);
      handle.writeBigUInt64LE(0xabcn);
      return failOpen;
    },
    waveOutPrepareHeader: (wave, header, size) => {
      calls.push(`prepare ${wave.toString(16)} ${size}`);
      if (headers.length === failPrepare) return 11;
      header.writeUInt32LE(header.readUInt32LE(24) | 2, 24);
      headers.push(header);
      return 0;
    },
    waveOutWrite: (_wave, header, size) => {
      calls.push(`write ${header.readUInt32LE(8)} ${size}`);
      return 0;
    },
    waveOutUnprepareHeader: () => {
      calls.push('unprepare');
      return 0;
    },
    waveOutReset: () => {
      calls.push('reset');
      for (const h of headers) h.writeUInt32LE(h.readUInt32LE(24) | 1, 24);
      return 0;
    },
    waveOutClose: () => {
      calls.push('close');
      return 0;
    },
    pointer: buffer => {
      blocks.set((next += 0x1000n), buffer);
      return next;
    },
  };
  return { api, calls, blocks, headers };
}

test('waveOut writes through a prepared ring, polls blocks done and closes in order', () => {
  const { api, calls, blocks, headers } = fakeWinMM();
  const out = new WaveOut(882, api);

  // The default device opens for 44.1 kHz 16-bit mono without a callback, and eight 882-byte blocks are prepared.
  assert.deepEqual(calls.slice(0, 2), ['open ffffffff 0 0100010044ac000088580100020010000000', 'prepare abc 48']);
  assert.equal(headers.length, 8);
  for (const h of headers) {
    assert.equal(blocks.get(h.readBigUInt64LE(0))!.length, 882);
    assert.equal(h.readUInt32LE(8), 882);
  }

  // A written block is copied into its slot and stays pending until winmm sets WHDR_DONE.
  const block = Buffer.alloc(882, 7);
  out.write(block);
  assert.equal(out.pending(), 1);
  assert.equal(calls.at(-1), 'write 882 48');
  const used = headers.find(h => blocks.get(h.readBigUInt64LE(0))![0] === 7)!;
  assert.deepEqual(blocks.get(used.readBigUInt64LE(0)), block);
  used.writeUInt32LE(used.readUInt32LE(24) | 1, 24);
  assert.equal(out.pending(), 0);

  // A reused slot has WHDR_DONE cleared before it's queued again, and a full ring refuses more.
  for (let i = 0; i < 8; i++) out.write(Buffer.alloc(441));
  assert.ok(headers.every(h => !(h.readUInt32LE(24) & 1)));
  assert.equal(out.pending(), 8);
  assert.throws(() => out.write(block), /no free audio block/);

  // Closing resets, unprepares every block and only then closes the device.
  out.close();
  assert.deepEqual(calls.slice(-10), ['reset', ...Array(8).fill('unprepare'), 'close']);
  assert.throws(() => out.write(block), /closed/);
  out.close();
  assert.equal(calls.at(-1), 'close');
  assert.equal(calls.filter(c => c === 'close').length, 1);

  // A device that won't open throws. One that fails partway through preparing undoes what it did.
  assert.throws(() => new WaveOut(882, fakeWinMM(2).api), /waveOutOpen failed with error 2/);
  const partial = fakeWinMM(0, 2);
  assert.throws(() => new WaveOut(882, partial.api), /waveOutPrepareHeader failed with error 11/);
  assert.deepEqual(partial.calls.slice(1), [
    'prepare abc 48',
    'prepare abc 48',
    'prepare abc 48',
    'reset',
    'unprepare',
    'unprepare',
    'close',
  ]);
});
