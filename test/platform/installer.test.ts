// Tests for the standalone installer, install/install.mjs. Each test runs it in a child
// process against a fake package, with a private home folder and a stubbed runtime probe.
// Some tests patch fs/promises in the child to record or break flushes and renames.

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, cp, rm, utimes, readdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { installationLock } from '../../src/platform/install-lock.js';
import { openRoot } from '../../src/platform/fs-root.js';

// Runs a program without a shell and collects stderr. It's killed after 30 seconds.
function run(program: string, args: string[], env: NodeJS.ProcessEnv): Promise<{ code: number; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(program, args, {
      env,
      shell: false,
      windowsHide: true,
      signal: AbortSignal.timeout(30000),
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    let stderr = '';
    child.stderr.on('data', bytes => {
      stderr += bytes;
    });
    child.on('error', reject);
    child.on('close', code => resolve({ code: code ?? 1, stderr }));
  });
}

const installer = fileURLToPath(new URL('../../../install/install.mjs', import.meta.url));

// Builds an unpacked package: a fake runtime, a stub CLI, the real built lock and filesystem
// modules the installer loads, and a manifest. The environment points every data folder at a
// private one and preloads a probe stub, because the fake runtime can't actually run.
async function fixture(t: test.TestContext) {
  const work = await mkdtemp(path.join(os.tmpdir(), 'frost-installer-'));
  t.after(() => rm(work, { recursive: true, force: true }));
  const source = path.join(work, 'package');
  const bin = path.join(work, 'bin with spaces');
  const version = 'v0.0.0-test';
  const scripts = path.join(source, 'versions', version, 'src');

  await mkdir(path.join(scripts, 'cli'), { recursive: true });
  await mkdir(path.join(scripts, 'platform'));
  await mkdir(path.join(source, 'runtime/bin'), { recursive: true });
  await writeFile(path.join(scripts, 'cli/main.js'), 'export {};\n');
  await writeFile(path.join(source, 'versions', version, 'package.json'), '{"type":"module"}\n');
  for (const name of ['install-lock', 'fs-root', 'fs-root-types', 'fs-root-windows', 'fs-root-posix', 'ffi-loader']) {
    await cp(
      fileURLToPath(new URL(`../../src/platform/${name}.js`, import.meta.url)),
      path.join(scripts, `platform/${name}.js`),
    );
  }

  const runtime = Buffer.from('unexecuted fixture runtime');
  const platform = process.platform === 'win32' ? 'windows' : process.platform;
  await writeFile(path.join(source, 'runtime/bin', process.platform === 'win32' ? 'node.exe' : 'node'), runtime);
  await writeFile(path.join(source, 'runtime/LICENSE'), 'fixture license');
  await writeFile(path.join(source, 'launch.mjs'), 'export {};\n');
  await writeFile(
    path.join(source, 'manifest.json'),
    JSON.stringify({
      version,
      os: platform,
      arch: process.arch === 'x64' ? 'amd64' : process.arch,
      nodeVersion: 'v26.10.0',
      nodeSha256: createHash('sha256').update(runtime).digest('hex'),
    }),
  );

  // The installer probes `node --version` (one argument) and then `node main.js --version`. The
  // stub answers each with the expected version without starting a process.
  const probe = path.join(work, 'fake-runtime-probe.mjs');
  await writeFile(
    probe,
    `import childProcess from 'node:child_process';\nimport { syncBuiltinESMExports } from 'node:module';\nchildProcess.execFile = (_program, args, _options, callback) => { queueMicrotask(() => callback(null, args.length === 1 ? 'v26.10.0\\n' : ${JSON.stringify('frost ' + version + '\n')}, '')); };\nsyncBuiltinESMExports();\n`,
  );

  const data = path.join(work, 'private data');
  const env = {
    ...process.env,
    LOCALAPPDATA: data,
    XDG_DATA_HOME: data,
    HOME: data,
    NODE_OPTIONS: '--import=' + pathToFileURL(probe).href,
  };
  const root =
    process.platform === 'darwin'
      ? path.join(data, 'Library/Application Support/frost/app')
      : path.join(data, 'frost/app');
  return { work, source, bin, version, env, root };
}

// One recorded filesystem step. `id` pairs a handle's sync with its close.
interface InstallEvent {
  kind: 'sync' | 'close' | 'rename';
  file: string;
  destination?: string;
  directory?: boolean;
  id?: number;
}

// Runs the installer through a bootstrap that records every sync, close and rename in order. With
// a boundary, the first directory sync on that folder throws instead, to test the failure path.
// The events are written to a file even when the installer fails.
async function recordInstaller(f: Awaited<ReturnType<typeof fixture>>, boundary = '') {
  const bootstrap = path.join(f.work, 'record-durability.mjs');
  const record = path.join(f.work, 'durability.json');
  await writeFile(
    bootstrap,
    `
import promises from 'node:fs/promises';
import {syncBuiltinESMExports} from 'node:module';
import path from 'node:path';
const originalOpen=promises.open,originalRename=promises.rename,events=[];let id=0,failed=false;
const root=${JSON.stringify(f.root)},bin=${JSON.stringify(f.bin)},boundary=${JSON.stringify(boundary)};
promises.open=async(...args)=>{const handle=await originalOpen(...args),file=String(args[0]),directory=(await handle.stat()).isDirectory(),current=++id,sync=handle.sync.bind(handle),close=handle.close.bind(handle);
  handle.sync=async()=>{events.push({kind:'sync',file,directory,id:current});const matches=boundary==='scripts'?file.endsWith(path.join('src','cli')):file===(boundary==='root'?root:boundary==='bin'?bin:path.join(root,boundary));
    if(boundary&&directory&&matches&&!failed){failed=true;throw new Error('injected directory durability failure')}await sync()};
  handle.close=async()=>{try{await close()}finally{events.push({kind:'close',file,directory,id:current})}};return handle};
promises.rename=async(...args)=>{await originalRename(...args);events.push({kind:'rename',file:String(args[0]),destination:String(args[1])})};
syncBuiltinESMExports();let failure;try{await import(${JSON.stringify(pathToFileURL(installer).href)})}catch(error){failure=error}finally{await promises.writeFile(${JSON.stringify(record)},JSON.stringify(events))}if(failure)throw failure;
`,
  );
  const result = await run(process.execPath, [bootstrap, f.source, f.bin], f.env);
  const events = JSON.parse(await readFile(record, 'utf8')) as InstallEvent[];
  return { result, events };
}

// A rerun of the same version succeeds even with an old lock marker naming a dead process. If the
// installed scripts were edited, though, the rerun refuses rather than mixing two versions.
test('standalone installer reuses an exact version and an unlocked persistent marker', async t => {
  const f = await fixture(t);
  const first = await run(process.execPath, [installer, f.source, f.bin], f.env);
  assert.equal(first.code, 0, first.stderr);

  const lock = path.join(f.root, '.frost-update.lock');
  const old = new Date(Date.now() - 700_000);
  await writeFile(lock, '2147483647\ndead-owner\n');
  await utimes(lock, old, old);
  const repeat = await run(process.execPath, [installer, f.source, f.bin], f.env);
  assert.equal(repeat.code, 0, repeat.stderr);
  assert.equal(JSON.parse(await readFile(path.join(f.root, 'current.json'), 'utf8')).version, f.version);

  await writeFile(path.join(f.root, 'versions', f.version, 'src/cli/main.js'), 'changed scripts');
  const changed = await run(process.execPath, [installer, f.source, f.bin], f.env);
  assert.notEqual(changed.code, 0);
  assert.match(changed.stderr, /scripts differ/);

  // Taking the lock afterwards proves the failed run released it.
  const unlock = await installationLock(f.root);
  await unlock();
});

// The bootstrap makes mkdtemp throw, so the installer fails right after taking its lock.
test('standalone installer releases its lock when staging cannot start', async t => {
  const f = await fixture(t);
  const bootstrap = path.join(f.work, 'refuse-stage.mjs');
  await writeFile(
    bootstrap,
    `import promises from 'node:fs/promises';\nimport { syncBuiltinESMExports } from 'node:module';\npromises.mkdtemp = async () => { throw new Error('staging refused'); };\nsyncBuiltinESMExports();\nawait import(${JSON.stringify(pathToFileURL(installer).href)});\n`,
  );
  const failed = await run(process.execPath, [bootstrap, f.source, f.bin], f.env);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /staging refused/);

  const unlock = await installationLock(f.root);
  await unlock();
  const retry = await run(process.execPath, [installer, f.source, f.bin], f.env);
  assert.equal(retry.code, 0, retry.stderr);
});

// This probe stub reports the wrong Node version, so nothing may be activated.
test('standalone installer refuses a failed runtime probe before activating files', async t => {
  const f = await fixture(t);
  const refuse = path.join(f.work, 'wrong-runtime-probe.mjs');
  await writeFile(
    refuse,
    `import childProcess from 'node:child_process';\nimport { syncBuiltinESMExports } from 'node:module';\nchildProcess.execFile = (_program, _args, _options, callback) => { queueMicrotask(() => callback(null, 'v0.0.0\\n', '')); };\nsyncBuiltinESMExports();\n`,
  );
  const failed = await run(process.execPath, [installer, f.source, f.bin], {
    ...f.env,
    NODE_OPTIONS: '--import=' + pathToFileURL(refuse).href,
  });

  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /new runtime says.*expected v26\.10\.0/);
  await assert.rejects(readFile(path.join(f.root, 'current.json')), { code: 'ENOENT' });
  await assert.rejects(readFile(path.join(f.root, 'runtime/bin', process.platform === 'win32' ? 'node.exe' : 'node')), {
    code: 'ENOENT',
  });
  const unlock = await installationLock(f.root);
  await unlock();
});

// During the application probe, the stub moves the installation root away and leaves a symlink to
// `outside` in its place. The installer must notice and write nothing through the link.
test(
  'POSIX standalone installer refuses a root replaced during the application probe',
  { skip: process.platform === 'win32' },
  async t => {
    const f = await fixture(t);
    const moved = path.join(f.work, 'moved-app');
    const outside = path.join(f.work, 'outside');
    const swap = path.join(f.work, 'swap-runtime-probe.mjs');
    await mkdir(f.root, { recursive: true });
    await mkdir(outside);
    await writeFile(path.join(f.root, 'current.json'), '{"version":"v0.0.0-old"}\n');
    await writeFile(
      swap,
      `import childProcess from 'node:child_process';import {rename,symlink} from 'node:fs/promises';import {syncBuiltinESMExports} from 'node:module';
childProcess.execFile=(_program,args,_options,callback)=>{if(args.length===1){queueMicrotask(()=>callback(null,'v26.10.0\\n',''));return}void(async()=>{await rename(${JSON.stringify(f.root)},${JSON.stringify(moved)});await symlink(${JSON.stringify(outside)},${JSON.stringify(f.root)});callback(null,${JSON.stringify('frost ' + f.version + '\n')},'')})().catch(callback)};syncBuiltinESMExports();`,
    );
    const result = await run(process.execPath, [installer, f.source, f.bin], {
      ...f.env,
      NODE_OPTIONS: '--import=' + pathToFileURL(swap).href,
    });

    assert.notEqual(result.code, 0);
    assert.match(result.stderr, /installation root changed/);
    assert.deepEqual(await readdir(outside), []);
    assert.equal(JSON.parse(await readFile(path.join(moved, 'current.json'), 'utf8')).version, 'v0.0.0-old');
    const unlock = await installationLock(moved);
    await unlock();
  },
);

// Checks the order of the recorded events. Every staged script folder is synced, src/cli before
// src, before the version folder is renamed into place. Then every installation folder, the
// launcher file and its folder are synced before current.json is renamed, which commits the install.
test(
  'POSIX standalone installation flushes script directories and the copied launcher before committing its pointer',
  { skip: process.platform === 'win32' },
  async t => {
    const f = await fixture(t);
    const { result, events } = await recordInstaller(f);
    assert.equal(result.code, 0, result.stderr);

    const versionIndex = events.findIndex(
      e => e.kind === 'rename' && e.destination === path.join(f.root, 'versions', f.version),
    );
    assert.ok(versionIndex >= 0);
    const staged = events[versionIndex].file;
    const commit = events.findIndex(e => e.kind === 'rename' && e.destination === path.join(f.root, 'current.json'));
    assert.ok(commit > versionIndex);

    // Staged script folders are flushed before the version folder moves into place.
    for (const folder of [
      path.join(staged, 'src/cli'),
      path.join(staged, 'src/platform'),
      path.join(staged, 'src'),
      staged,
    ]) {
      assert.ok(
        events.some(
          (event, index) => index < versionIndex && event.kind === 'sync' && event.directory && event.file === folder,
        ),
        folder,
      );
    }
    const sourceParent = events.findIndex(e => e.kind === 'sync' && e.directory && e.file === path.join(staged, 'src'));
    assert.ok(
      events.findIndex(e => e.kind === 'sync' && e.directory && e.file === path.join(staged, 'src/cli')) < sourceParent,
    );

    // Installation folders are flushed between that rename and the commit.
    for (const folder of [
      path.join(f.root, 'versions'),
      path.join(f.root, 'runtime/bin'),
      path.join(f.root, 'runtime'),
      f.root,
      f.bin,
    ]) {
      assert.ok(
        events.some(
          (event, index) =>
            index > versionIndex && index < commit && event.kind === 'sync' && event.directory && event.file === folder,
        ),
        folder,
      );
    }

    // The launcher file is flushed before its rename, and its folder after it.
    const launcher = events.findIndex(e => e.kind === 'rename' && e.destination === path.join(f.bin, 'frost'));
    assert.ok(launcher >= 0 && launcher < commit);
    assert.ok(
      events.some(
        (e, index) => index < launcher && e.kind === 'sync' && !e.directory && e.file === events[launcher].file,
      ),
    );
    assert.ok(
      events.some(
        (e, index) => index > launcher && index < commit && e.kind === 'sync' && e.directory && e.file === f.bin,
      ),
    );

    // Every folder handle that was synced was also closed.
    for (const event of events)
      if (event.kind === 'sync' && event.directory)
        assert.ok(events.some(e => e.kind === 'close' && e.id === event.id));
  },
);

// Fails the first directory sync at each boundary in turn. The old current.json must survive,
// every synced handle must be closed and the lock must be free afterwards.
test(
  'POSIX standalone installer directory-sync failures keep the prior pointer and release the native lock',
  { skip: process.platform === 'win32' },
  async t => {
    for (const boundary of ['scripts', 'versions', 'runtime/bin', 'runtime', 'root', 'bin'])
      await t.test(boundary, async t => {
        const f = await fixture(t);
        await mkdir(f.root, { recursive: true });
        await writeFile(path.join(f.root, 'current.json'), '{"version":"v0.0.0-old"}\n');
        const { result, events } = await recordInstaller(f, boundary);

        assert.notEqual(result.code, 0);
        assert.match(result.stderr, /injected directory durability failure/);
        assert.ok(!events.some(e => e.kind === 'rename' && e.destination === path.join(f.root, 'current.json')));
        for (const event of events)
          if (event.kind === 'sync' && event.directory)
            assert.ok(events.some(e => e.kind === 'close' && e.id === event.id));
        assert.equal(JSON.parse(await readFile(path.join(f.root, 'current.json'), 'utf8')).version, 'v0.0.0-old');
        const unlock = await installationLock(f.root);
        await unlock();
      });
  },
);

// The bootstrap makes a rename into frost.cmd from another folder fail with EXDEV, as it would
// across drives. The launcher must be staged in the launcher folder itself, so it still succeeds.
test(
  'Windows launcher activation stages its replacement on the destination drive',
  { skip: process.platform !== 'win32' },
  async t => {
    const f = await fixture(t);
    const bootstrap = path.join(f.work, 'cross-drive.mjs');
    await writeFile(
      bootstrap,
      `import promises from 'node:fs/promises';\nimport { syncBuiltinESMExports } from 'node:module';\nimport path from 'node:path';\nconst rename = promises.rename;\npromises.rename = async (source, destination) => { if (destination === path.join(process.argv[3], 'frost.cmd') && path.dirname(source) !== path.dirname(destination)) throw Object.assign(new Error('cross-device rename'), { code: 'EXDEV' }); return rename(source, destination); };\nsyncBuiltinESMExports();\nawait import(${JSON.stringify(pathToFileURL(installer).href)});\n`,
    );
    const result = await run(process.execPath, [bootstrap, f.source, f.bin], f.env);
    assert.equal(result.code, 0, result.stderr);
    assert.match(await readFile(path.join(f.bin, 'frost.cmd'), 'utf8'), /runtime\\bin\\node.exe/);
  },
);

// mkdtemp is pinned to a known name, so the launcher's staging path is predictable. A file or a
// symlink planted there first must make the install fail with EEXIST, leaving both untouched.
test('standalone installer exclusively creates the launcher staging leaf and never overwrites its link target', async t => {
  for (const suffix of process.platform === 'win32' ? ['', '.cmd'] : [''])
    for (const kind of ['file', 'symlink'])
      await t.test(kind + suffix, async t => {
        const f = await fixture(t);
        const leaf = '.frost-install-.frost-install-fixed-for-test' + suffix;
        const temporary = path.join(f.bin, leaf);
        const sentinel = path.join(f.work, 'outside-sentinel');
        const bootstrap = path.join(f.work, 'fixed-stage.mjs');
        await mkdir(f.bin);
        await writeFile(sentinel, 'unchanged outside');
        if (kind === 'file') await writeFile(temporary, 'unchanged staging entry');
        else {
          const root = await openRoot(f.bin);
          try {
            root.symlink(sentinel, leaf, false);
          } finally {
            root.close();
          }
        }

        await writeFile(
          bootstrap,
          `import promises from 'node:fs/promises';import {syncBuiltinESMExports} from 'node:module';promises.mkdtemp=async prefix=>{const dir=prefix+'fixed-for-test';await promises.mkdir(dir,{mode:0o700});return dir};syncBuiltinESMExports();await import(${JSON.stringify(pathToFileURL(installer).href)});`,
        );
        const result = await run(process.execPath, [bootstrap, f.source, f.bin], f.env);

        assert.notEqual(result.code, 0);
        assert.match(result.stderr, /EEXIST/);
        assert.equal(await readFile(sentinel, 'utf8'), 'unchanged outside');
        assert.equal(
          await readFile(temporary, 'utf8'),
          kind === 'file' ? 'unchanged staging entry' : 'unchanged outside',
        );
        assert.deepEqual(await readdir(f.bin), [leaf]);
        await assert.rejects(readFile(path.join(f.root, 'current.json')), { code: 'ENOENT' });
        const unlock = await installationLock(f.root);
        await unlock();
      });
});

// The data path holds quotes and `!`, which a careless sh or cmd launcher would mangle. The sh
// launcher is for Git Bash and similar shells, and the cmd launcher turns off delayed expansion.
test(
  'Windows installation creates launchers for both native terminals and POSIX shells',
  { skip: process.platform !== 'win32' },
  async t => {
    const f = await fixture(t);
    const data = path.join(f.work, "private data with 'quotes' and !bangs");
    const root = path.join(data, 'frost/app');
    const result = await run(process.execPath, [installer, f.source, f.bin], { ...f.env, LOCALAPPDATA: data });
    assert.equal(result.code, 0, result.stderr);

    const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";
    const node = path.join(root, 'runtime/bin/node.exe');
    const launch = path.join(root, 'launch.mjs');
    assert.equal(
      await readFile(path.join(f.bin, 'frost'), 'utf8'),
      `#!/bin/sh\nexec ${quote(node.replaceAll('\\', '/'))} ${quote(launch.replaceAll('\\', '/'))} "$@"\n`,
    );
    assert.equal(
      await readFile(path.join(f.bin, 'frost.cmd'), 'utf8'),
      `@echo off\r\nsetlocal DisableDelayedExpansion\r\n"${node}" "${launch}" %*\r\n`,
    );
    assert.equal(JSON.parse(await readFile(path.join(root, 'current.json'), 'utf8')).version, f.version);
    assert.deepEqual((await readdir(f.bin)).sort(), ['frost', 'frost.cmd']);
  },
);

// Records opens and syncs in the launcher folder plus every rename. Both launchers must be created
// exclusively ('wx') and flushed before the first one is renamed into place.
test(
  'Windows installer stages both launchers before activating either',
  { skip: process.platform !== 'win32' },
  async t => {
    const f = await fixture(t);
    const bootstrap = path.join(f.work, 'record-launchers.mjs');
    const record = path.join(f.work, 'launcher-events.json');
    await writeFile(
      bootstrap,
      `import promises from 'node:fs/promises';import path from 'node:path';import {syncBuiltinESMExports} from 'node:module';
const open=promises.open,rename=promises.rename,events=[],bin=${JSON.stringify(f.bin)};
promises.open=async(...args)=>{const result=await open(...args);if(path.dirname(String(args[0]))===bin){events.push({kind:'open',file:String(args[0]),flags:args[1]});const sync=result.sync.bind(result);result.sync=async()=>{await sync();events.push({kind:'sync',file:String(args[0])})}}return result};
promises.rename=async(...args)=>{await rename(...args);events.push({kind:'rename',file:String(args[0]),destination:String(args[1])})};syncBuiltinESMExports();try{await import(${JSON.stringify(pathToFileURL(installer).href)})}finally{await promises.writeFile(${JSON.stringify(record)},JSON.stringify(events))}`,
    );
    const result = await run(process.execPath, [bootstrap, f.source, f.bin], f.env);
    assert.equal(result.code, 0, result.stderr);

    const events = JSON.parse(await readFile(record, 'utf8')) as (Omit<InstallEvent, 'kind'> & {
      kind: InstallEvent['kind'] | 'open';
      flags?: string;
    })[];
    const firstActivation = events.findIndex(
      e =>
        e.kind === 'rename' && [path.join(f.bin, 'frost.cmd'), path.join(f.bin, 'frost')].includes(e.destination ?? ''),
    );
    const commit = events.findIndex(e => e.kind === 'rename' && e.destination === path.join(f.root, 'current.json'));
    const writes = events.filter(e => e.kind === 'open');
    assert.equal(writes.length, 2);
    for (const write of writes) {
      assert.equal(write.flags, 'wx');
      const sync = events.findIndex(e => e.kind === 'sync' && e.file === write.file);
      assert.ok(sync >= 0 && sync < firstActivation);
    }
    assert.ok(firstActivation >= 0 && firstActivation < commit);
  },
);
