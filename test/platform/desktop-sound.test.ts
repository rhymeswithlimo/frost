// Tests for desktop actions (opening URLs, revealing files, picking folders) with stubbed command
// runners, and for the sound effects and WAV codec. Nothing opens a browser or plays audio.

import test from 'node:test';
import assert from 'node:assert/strict';
import { available, openCommand, revealCommand, openBrowser, pickFolder } from '../../src/platform/desktop.js';
import { Player, decodeWAV, encodeWAV, resample, vary, cut, effects, sampleRate } from '../../src/platform/sound.js';

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
