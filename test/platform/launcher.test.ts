// Tests the launcher every package ships, install/launch.mjs. An update never replaces it, so an install keeps the one
// it was installed with and it has to start every version folder that install will ever hold.

import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFile, mkdir, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import os from 'node:os';
import path from 'node:path';
import { run } from '../../src/platform/command.js';

// Tests run from dist/test/platform, so the project is three folders up.
const project = fileURLToPath(new URL('../../../', import.meta.url));

// An installation with the launcher and one version folder whose CLI reports what it was started with. The temp folder
// is resolved first, since the launcher works from the real path.
async function installation(t: test.TestContext, versions: string[], current?: string) {
  const root = await realpath(await mkdtemp(path.join(os.tmpdir(), 'frost-launcher-')));
  t.after(() => rm(root, { recursive: true, force: true }));
  await copyFile(path.join(project, 'install/launch.mjs'), path.join(root, 'launch.mjs'));
  for (const version of versions) {
    await mkdir(path.join(root, 'versions', version, 'src/cli'), { recursive: true });
    await writeFile(
      path.join(root, 'versions', version, 'src/cli/main.js'),
      `console.log(JSON.stringify({ version: ${JSON.stringify(version)}, root: process.env.FROST_APP_ROOT, args: process.argv.slice(2) }));\n`,
    );
  }
  if (current !== undefined) await writeFile(path.join(root, 'current.json'), current);
  return { root, launch: path.join(root, 'launch.mjs') };
}

test('the launcher starts the version current.json names, with its arguments and the app root', async t => {
  const f = await installation(t, ['v1.2.3', 'v2.0.0-rc.1'], '{"version":"v2.0.0-rc.1"}\n');
  const result = await run(process.execPath, [f.launch, 'backup', '--scheduled']);
  assert.equal(result.code, 0, result.stderr);
  const started = JSON.parse(result.stdout);
  assert.equal(started.version, 'v2.0.0-rc.1');
  assert.deepEqual(started.args, ['backup', '--scheduled']);
  assert.equal(path.join(started.root, 'launch.mjs'), f.launch);
});

// current.json is data on disk, so its version can't be allowed to point outside versions/.
test('the launcher refuses a version that is not a release tag', async t => {
  for (const version of ['../elsewhere', 'v1.2', 'dev', 'v1.2.3/../../x', 'v01.2.3', 'v1.2.3+build']) {
    const f = await installation(t, ['v1.2.3'], JSON.stringify({ version }));
    const result = await run(process.execPath, [f.launch]);
    assert.notEqual(result.code, 0, version);
    assert.match(result.stderr, /Invalid installed version/, version);
    assert.equal(result.stdout, '', version);
  }
});

test('the launcher fails when there is no current.json', async t => {
  const f = await installation(t, ['v1.2.3']);
  const result = await run(process.execPath, [f.launch]);
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /current\.json/);
});
