// Tests the TUI demo's fake repository, its flags and slow backend, and the packaged wordmarks and sounds.

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { createHash } from 'node:crypto';
import { Key } from '../../src/core/crypto.js';
import { Repo } from '../../src/core/repo.js';
import { Manifest } from '../../src/core/manifest.js';
import { Engine } from '../../src/engine/index.js';
import { MemoryBackend, buildHistory, breakThings, demoHost, parseDemoArgs, SlowBackend } from '../../src/tui/demo.js';
import { decodeWAV } from '../../src/platform/sound.js';

// The demo backs up a generated home folder eight times into memory, then rewrites each snapshot's time to spread
// them over two weeks.
test('demo history backs up eight revisions, backdates them, restores, and exposes corruption', async t => {
  const work = await mkdtemp(path.join(os.tmpdir(), 'frost-demo-test-'));
  t.after(() => rm(work, { recursive: true, force: true }));
  const memory = new MemoryBackend();
  const repo = await Repo.init(memory, Key.new());
  const manifest = await Manifest.open(path.join(work, 'cache.jsonl'));
  const engine = new Engine(repo, manifest);
  t.after(() => manifest.close());
  const source = path.join(work, 'home');
  const now = Date.now();

  await buildHistory(engine, source, now);

  const snaps = (await repo.snapshots()).sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
  assert.equal(snaps.length, 8);
  assert.equal(Date.parse(snaps[0].time), now - 14 * 24 * 3600_000);
  assert.equal(Date.parse(snaps.at(-1)!.time), now - 3 * 3600_000);
  for (const snap of snaps) {
    assert.equal(snap.host, demoHost);
    assert.equal(manifest.snapshots().get(snap.id)!.time, snap.time);
    const tree = await repo.loadTree(snap.id);
    assert.ok(tree.files.length);
    // Each rewritten snapshot is still in storage, so saving it again is refused.
    await assert.rejects(repo.saveSnapshot(snap, tree), /already exists/);
  }
  assert.deepEqual((await engine.verify(manifest.chunkCount())).failures, []);

  // Restore the newest snapshot and compare every file with its source.
  const target = path.join(work, 'restored');
  await new Engine(repo).restore(snaps.at(-1)!.id, { target });
  const newest = await repo.loadTree(snaps.at(-1)!.id);
  for (const file of newest.files)
    if (file.type === 'file') {
      // Restore nests each absolute path under the target, without its drive colon or leading slashes.
      const expected = await readFile(file.path),
        restored = await readFile(
          path.join(
            target,
            file.path
              .replaceAll('\\', '/')
              .replace(/^([A-Za-z]):/, '$1')
              .replace(/^\/+/, ''),
          ),
        );
      assert.deepEqual(restored, expected);
    }

  // breakThings corrupts a few chunks, then records a failed verification and a failed backup for -broken to show.
  await breakThings(engine, memory);
  assert.ok(engine.lastVerify()!.failures!.length);
  assert.ok(engine.lastBackup()!.error);
});

// Latency takes Go-style durations such as '1m2.5s', and switches accept one or two dashes.
test('demo options and delayed backend honor durations, switches, and cancellation', async () => {
  assert.deepEqual(parseDemoArgs(['-latency', '400ms', '-empty', '-broken=false', '--setup']), {
    latency: 400,
    empty: true,
    broken: false,
    setup: true,
  });
  assert.equal(parseDemoArgs(['-latency=1m2.5s']).latency, 62500);
  assert.equal(parseDemoArgs(['-latency=0']).latency, 0);
  assert.equal(parseDemoArgs(['-latency=-2ms']).latency, -2);
  assert.throws(() => parseDemoArgs(['-latency=bad']), /parse error/);
  assert.throws(() => parseDemoArgs(['-unknown']), /not defined/);

  // Aborting a slow request rejects it straight away instead of after the full latency.
  const slow = new SlowBackend(new MemoryBackend(), 1000);
  const controller = new AbortController();
  const pending = slow.list('', controller.signal);
  controller.abort();
  await assert.rejects(pending, { name: 'AbortError' });
});

// assets.json records the SHA-256 of each asset it lists. Sound effects must also decode.
test('wordmarks and sound effects retain captured bytes and valid audio', async () => {
  const recorded = JSON.parse(
    await readFile(new URL('../../../test/fixtures/assets.json', import.meta.url), 'utf8'),
  ) as Record<string, string>;
  for (const [file, hash] of Object.entries(recorded)) {
    const bytes = await readFile(new URL('../../../assets/' + file, import.meta.url));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), hash, file);
    if (file.endsWith('.wav')) assert.ok(decodeWAV(bytes).length);
  }
});
