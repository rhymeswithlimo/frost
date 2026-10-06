// Tests the maintainer tools in tools/: the build's output cleanup, the pinned runtime and dependency checks, and the
// storage fixture the package smoke test runs against. Tools that check bytes run in child processes.

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink, lstat } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';
import path from 'node:path';
import os from 'node:os';
import { run } from '../../src/platform/command.js';

// Tests run from dist/test/platform, so the project is three folders up.
const project = fileURLToPath(new URL('../../../', import.meta.url));
const tools = path.join(project, 'tools');
const tool = (name: string) => import(pathToFileURL(path.join(tools, name)).href);

test('build cleanup removes old output and keeps project files', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'frost-build-tools-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const { cleanBuild } = await tool('build.mjs');
  await mkdir(path.join(root, 'dist/src'), { recursive: true });
  await writeFile(path.join(root, 'dist/src/deleted.js'), 'old compiled code');
  await writeFile(path.join(root, 'source.ts'), 'current source');

  await cleanBuild(root);
  await assert.rejects(lstat(path.join(root, 'dist')), { code: 'ENOENT' });
  assert.equal(await readFile(path.join(root, 'source.ts'), 'utf8'), 'current source');

  // Cleaning again with no dist/ is fine.
  await cleanBuild(root);
});

// A dist/ that links elsewhere must never lead the cleanup outside the project.
test('build cleanup refuses a linked output folder', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'frost-build-tools-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const { cleanBuild } = await tool('build.mjs');
  const outside = path.join(root, 'outside');
  await mkdir(outside);
  await writeFile(path.join(outside, 'keep.txt'), 'keep');
  await symlink(outside, path.join(root, 'dist'), process.platform === 'win32' ? 'junction' : 'dir');
  await assert.rejects(cleanBuild(root), /must be a directory inside the project/);
  assert.equal(await readFile(path.join(outside, 'keep.txt'), 'utf8'), 'keep');
});

// A runtime whose bytes don't match the pinned executable is refused for every target, before anything is packaged.
test('packaging refuses a runtime that differs from the pin', async t => {
  const work = await mkdtemp(path.join(os.tmpdir(), 'frost-package-runtime-'));
  t.after(() => rm(work, { recursive: true, force: true }));
  const lock = JSON.parse(await readFile(path.join(tools, 'runtime-lock.json'), 'utf8'));
  const runtime = path.join(work, 'node');
  await writeFile(runtime, 'edited runtime');
  for (const target of Object.keys(lock.artifacts)) {
    const result = await run(process.execPath, [
      path.join(tools, 'package.mjs'),
      '--version',
      'v0.0.0-test',
      '--platform',
      target,
      '--runtime',
      runtime,
      '--out',
      path.join(work, 'out'),
    ]);
    assert.notEqual(result.code, 0);
    assert.match(result.stderr, /pinned Node executable/);
  }
});

// A hand-written package.json for @iarna/toml can't match the files in the archive the lock pins.
test('dependency audit rejects installed bytes that differ from the pinned archive', async t => {
  const work = await mkdtemp(path.join(os.tmpdir(), 'frost-dependency-audit-'));
  t.after(() => rm(work, { recursive: true, force: true }));
  await writeFile(path.join(work, 'package-lock.json'), await readFile(path.join(project, 'package-lock.json')));
  const dependency = path.join(work, 'node_modules/@iarna/toml');
  await mkdir(dependency, { recursive: true });
  await writeFile(path.join(dependency, 'package.json'), '{"name":"@iarna/toml","version":"2.2.5"}');
  const result = await run(process.execPath, [path.join(tools, 'dependencies.mjs'), work]);
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /differs from the pinned archive/);
});

// A lock without @iarna/toml, and an empty one, would let the audit pass by checking less.
test('dependency audit refuses a lock that omits a reviewed dependency', async t => {
  const work = await mkdtemp(path.join(os.tmpdir(), 'frost-dependency-audit-'));
  t.after(() => rm(work, { recursive: true, force: true }));
  const lock = JSON.parse(await readFile(path.join(project, 'package-lock.json'), 'utf8'));
  for (const packages of [
    Object.fromEntries(Object.entries(lock.packages).filter(([name]) => name !== 'node_modules/@iarna/toml')),
    {},
  ]) {
    await writeFile(path.join(work, 'package-lock.json'), JSON.stringify({ ...lock, packages }));
    const result = await run(process.execPath, [path.join(tools, 'dependencies.mjs'), work]);
    assert.notEqual(result.code, 0);
    assert.match(result.stderr, /every reviewed package exactly once/);
  }
});

// The smoke test's fixture serves a fresh repository over loopback Permafrost. A backup through the CLI must save one
// snapshot that authenticates, without printing the recovery phrase.
test('smoke fixture accepts a backup and authenticates the saved snapshot', async t => {
  const { smokeFixture } = await tool('smoke.mjs');
  const work = await mkdtemp(path.join(os.tmpdir(), 'frost-smoke-fixture-'));
  t.after(() => rm(work, { recursive: true, force: true }));
  const fixture = await smokeFixture(work);
  t.after(() => fixture.close());
  await assert.rejects(fixture.verify(), /No snapshot was saved/);

  const result = await run(process.execPath, [
    path.join(project, 'dist/src/cli/main.js'),
    '--config-dir',
    fixture.config,
    '--cache-dir',
    path.join(work, 'cache'),
    'backup',
    '--no-verify',
  ]);
  assert.equal(result.code, 0, result.stderr);
  assert.equal(await fixture.verify(), 1);
  assert.doesNotMatch(result.stdout + result.stderr, /abandon abandon|recovery phrase/);
});
