// Tests for the installation lock shared by the installer and updater, runtime reuse during
// activation, and the ownership checks on the installation folder's ancestors.

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, rm, utimes } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import * as fs from 'node:fs';
import { installationDirectory, installationLock } from '../../src/platform/install-lock.js';
import { replaceRuntime } from '../../src/platform/update.js';

// Ownership comes from the OS lock, so a marker naming a dead PID neither blocks a new owner nor
// gets rewritten by one.
test('installation locks exclude concurrent owners without trusting marker contents', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'frost-install-lock-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const name = path.join(root, '.frost-update.lock');
  const unlock = await installationLock(root);
  await assert.rejects(installationLock(root), /already running/);
  await unlock();

  await writeFile(name, '2147483647\ndead-owner\n');
  const recovered = await installationLock(root);
  await assert.rejects(installationLock(root), /already running/);
  await recovered();
  assert.equal(await readFile(name, 'utf8'), '2147483647\ndead-owner\n');
});
// A child process takes the lock, prints "locked" and idles. Once it's killed, the lock must be
// free again without any cleanup.
test('installation locks are released by process termination', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'frost-install-exit-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const module = new URL('../../src/platform/install-lock.js', import.meta.url).href;
  const child = spawn(
    process.execPath,
    [
      '--input-type=module',
      '-e',
      `import { installationLock } from ${JSON.stringify(module)}; await installationLock(process.argv[1]); process.stdout.write('locked'); setInterval(() => {}, 1000);`,
      root,
    ],
    { shell: false, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  t.after(() => {
    if (child.exitCode === null) child.kill('SIGKILL');
  });

  // Wait for "locked", failing if the child errors, exits first or takes longer than 15 seconds.
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error('lock owner did not start'));
    }, 15000);
    let output = '';
    let errors = '';
    child.stderr.on('data', data => {
      errors += data;
    });
    child.stdout.on('data', data => {
      output += data;
      if (output.includes('locked')) {
        clearTimeout(timeout);
        resolve();
      }
    });
    child.once('error', error => {
      clearTimeout(timeout);
      reject(error);
    });
    child.once('exit', code => {
      clearTimeout(timeout);
      reject(new Error(`lock owner exited ${code}: ${errors}`));
    });
  });

  await assert.rejects(installationLock(root), /already running/);
  await new Promise<void>(resolve => {
    child.once('close', () => resolve());
    child.kill('SIGKILL');
  });

  // Releasing twice is harmless.
  const unlock = await installationLock(root);
  await unlock();
  await unlock();
});

// When the staged runtime has the same bytes, the installed file isn't replaced, which the old
// mtime proves. The staged copy is still removed.
test('an identical runtime stays in place during activation', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'frost-runtime-reuse-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const staged = path.join(root, 'staged');
  const target = path.join(root, 'node');
  await writeFile(staged, 'same reviewed runtime');
  await writeFile(target, 'same reviewed runtime');
  const old = new Date('2001-01-01T00:00:00Z');
  await utimes(target, old, old);

  await replaceRuntime(staged, target);
  assert.equal(await readFile(target, 'utf8'), 'same reviewed runtime');
  await assert.rejects(readFile(staged), { code: 'ENOENT' });
  const { stat } = await import('node:fs/promises');
  assert.equal((await stat(target)).mtimeMs, old.getTime());
});

// A world-writable parent without the sticky bit would let another user swap the app folder.
// The app folder itself must never be writable by others, even with the sticky bit.
test(
  'POSIX installation ancestry refuses replaceable parents and writable application roots',
  { skip: process.platform === 'win32' },
  async t => {
    const base = await mkdtemp(path.join(os.tmpdir(), 'frost-install-ancestry-'));
    t.after(() => rm(base, { recursive: true, force: true }));
    const parent = path.join(base, 'parent');
    const root = path.join(parent, 'app');
    fs.mkdirSync(parent);
    fs.mkdirSync(root, { mode: 0o700 });

    fs.chmodSync(parent, 0o777);
    await assert.rejects(installationLock(root), /untrusted installation folder/);
    assert.deepEqual(fs.readdirSync(root), []);

    fs.chmodSync(parent, 0o1777);
    const unlock = await installationLock(root);
    await unlock.check();
    await unlock();

    fs.chmodSync(root, 0o1777);
    await assert.rejects(installationLock(root), /untrusted installation folder/);
    fs.chmodSync(root, 0o700);

    // Folders created along the way are private.
    const created = await installationDirectory(path.join(root, 'new', 'app'), true);
    created.close();
    assert.equal(fs.statSync(path.join(root, 'new')).mode & 0o777, 0o700);
  },
);

// Runs only as root, since it hands the parent to uid 65534 (nobody). That user could replace the
// app folder whatever the mode, so the lock is refused and nothing is written.
test(
  'POSIX installation ancestry refuses foreign owners even when sticky protects a current-user child',
  { skip: process.platform === 'win32' || process.getuid?.() !== 0 },
  async t => {
    const base = await mkdtemp(path.join(os.tmpdir(), 'frost-install-owner-'));
    t.after(() => rm(base, { recursive: true, force: true }));
    const parent = path.join(base, 'foreign');
    const root = path.join(parent, 'app');
    fs.mkdirSync(parent);
    fs.mkdirSync(root, { mode: 0o700 });

    // Some filesystems can't change or keep ownership. That's a test limit, so skip rather than fail.
    try {
      fs.chownSync(parent, 65534, 65534);
    } catch (error) {
      if (['EPERM', 'ENOTSUP', 'EOPNOTSUPP'].includes((error as NodeJS.ErrnoException).code ?? '')) {
        t.skip('filesystem cannot change private test ownership');
        return;
      }
      throw error;
    }
    if (fs.statSync(parent).uid !== 65534) {
      t.skip('filesystem does not retain private test ownership');
      return;
    }

    for (const mode of [0o755, 0o1777]) {
      fs.chmodSync(parent, mode);
      await assert.rejects(installationLock(root), /untrusted installation folder/);
    }
    assert.deepEqual(fs.readdirSync(root), []);
  },
);
