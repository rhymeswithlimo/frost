// Tests for the node:ffi loader. It hides Node's one experimental FFI warning while loading and
// must leave every other warning alone. Each test runs in a child process so the patched
// process.emitWarning and module loader can't leak into the test runner.

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const loader = new URL('../../src/platform/ffi-loader.js', import.meta.url).href;

// The child loads FFI twice (the second call returns the cached module), then emits an unrelated
// warning that must still reach stderr.
test('native API warning is scoped to builtin loading and emitWarning is restored', () => {
  const result = spawnSync(
    process.execPath,
    [
      '--input-type=module',
      '-e',
      `
    import assert from 'node:assert/strict';
    import {loadFFI} from ${JSON.stringify(loader)};
    const original=process.emitWarning;
    const ffi=loadFFI();assert.equal(typeof ffi.dlopen,'function');
    assert.equal(process.emitWarning,original);assert.equal(loadFFI(),ffi);
    process.emitWarning('unrelated runtime warning','ExperimentalWarning');
  `,
    ],
    { encoding: 'utf8', windowsHide: true },
  );
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stderr, /FFI is an experimental feature/);
  assert.match(result.stderr, /ExperimentalWarning: unrelated runtime warning/);
});

// The child fakes a failing node:ffi load that emits four warnings. Only the exact FFI warning
// is dropped. The other three, including the same text with another code or type, are forwarded.
// A later load without the fake must still work.
test('native API loading failure restores warnings and forwards every other warning', () => {
  const result = spawnSync(
    process.execPath,
    [
      '--input-type=module',
      '-e',
      `
    import assert from 'node:assert/strict';import {createRequire} from 'node:module';
    import {loadFFI} from ${JSON.stringify(loader)};
    const Module=createRequire(import.meta.url)('node:module'), originalLoad=Module._load;
    const forwarded=[];const originalWarning=(...args)=>forwarded.push(args);process.emitWarning=originalWarning;
    Module._load=function(...args){if(args[0]==='node:ffi'){
      const text='FFI is an experimental feature and might change at any time';
      process.emitWarning(text,'ExperimentalWarning');
      process.emitWarning(text,'ExperimentalWarning','OTHER_CODE');
      process.emitWarning(text,'Warning');
      process.emitWarning('another native warning','ExperimentalWarning');
      throw new Error('load failed');
    }return Reflect.apply(originalLoad,this,args)};
    assert.throws(loadFFI,/load failed/);assert.equal(process.emitWarning,originalWarning);
    assert.equal(forwarded.length,3);assert.equal(forwarded[0][2],'OTHER_CODE');
    assert.equal(forwarded[1][1],'Warning');assert.equal(forwarded[2][0],'another native warning');
    Module._load=originalLoad;assert.equal(typeof loadFFI().dlopen,'function');
    assert.equal(process.emitWarning,originalWarning);
  `,
    ],
    { encoding: 'utf8', windowsHide: true },
  );
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, '');
});
