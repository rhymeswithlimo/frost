// Tests for Windows console ANSI setup against a fake console API. No native calls are made.

import test from 'node:test';
import assert from 'node:assert/strict';
import { enableANSI, type ConsoleAPI } from '../../src/platform/console.js';

// A fake kernel32 console. Handle 11n is stdout and 12n is stderr (GetStdHandle gets -11 and -12).
// It records every mode change and every mode query, and starts with the given modes.
function consoleAPI(
  modes = new Map([
    [11n, 1],
    [12n, 3],
  ]),
) {
  const calls: [bigint, number][] = [];
  const queries: bigint[] = [];
  const api: ConsoleAPI = {
    getStdHandle: which => BigInt(-which),
    getConsoleMode: handle => {
      queries.push(handle);
      return modes.get(handle);
    },
    setConsoleMode: (handle, mode) => {
      calls.push([handle, mode]);
      modes.set(handle, mode);
      return true;
    },
  };
  return { api, calls, queries, modes };
}

// Enabling adds the virtual terminal flag (4) to each mode. Restoring runs once, in reverse.
test('Windows console processing changes only terminal outputs and restores original modes once in reverse order', () => {
  const f = consoleAPI();
  const state = enableANSI({ platform: 'win32', stdoutTTY: true, stderrTTY: true, api: f.api });
  assert.equal(state.ansiOK, true);
  assert.deepEqual(f.calls, [
    [11n, 5],
    [12n, 7],
  ]);

  state.restore();
  state.restore();
  assert.deepEqual(f.calls, [
    [11n, 5],
    [12n, 7],
    [12n, 3],
    [11n, 1],
  ]);
  assert.deepEqual(
    [...f.modes],
    [
      [11n, 1],
      [12n, 3],
    ],
  );

  // A redirected stdout is never queried.
  const single = consoleAPI();
  enableANSI({ platform: 'win32', stdoutTTY: false, stderrTTY: true, api: single.api }).restore();
  assert.deepEqual(single.queries, [12n]);
});

test('failed mode setting disables ANSI while already enabled and non-console outputs remain untouched', () => {
  // stderr refuses the new mode, so ANSI is off and only stdout needs restoring.
  const f = consoleAPI();
  f.api.setConsoleMode = (handle, mode) => {
    f.calls.push([handle, mode]);
    if (handle === 12n) return false;
    f.modes.set(handle, mode);
    return true;
  };
  const state = enableANSI({ platform: 'win32', stdoutTTY: true, stderrTTY: true, api: f.api });
  assert.equal(state.ansiOK, false);
  state.restore();
  assert.deepEqual(f.calls, [
    [11n, 5],
    [12n, 7],
    [11n, 1],
  ]);

  // stdout already has the flag and stderr isn't a console, so nothing is changed.
  const ready = consoleAPI(new Map([[11n, 5]]));
  const untouched = enableANSI({ platform: 'win32', stdoutTTY: true, stderrTTY: true, api: ready.api });
  assert.equal(untouched.ansiOK, true);
  untouched.restore();
  assert.deepEqual(ready.calls, []);
});

test('non-Windows and redirected output do not load or call native console APIs; restoration continues after failures', () => {
  const f = consoleAPI();
  for (const options of [
    { platform: 'linux', stdoutTTY: true, stderrTTY: true },
    { platform: 'win32', stdoutTTY: false, stderrTTY: false },
  ]) {
    const state = enableANSI({ ...options, api: f.api });
    assert.equal(state.ansiOK, true);
    state.restore();
  }
  assert.deepEqual(f.queries, []);

  // A throw while restoring stderr mustn't stop stdout being restored.
  const failure = consoleAPI();
  const state = enableANSI({ platform: 'win32', stdoutTTY: true, stderrTTY: true, api: failure.api });
  failure.api.setConsoleMode = (handle, mode) => {
    failure.calls.push([handle, mode]);
    if (handle === 12n) throw new Error('console closed');
    return true;
  };
  state.restore();
  assert.deepEqual(failure.calls.slice(-2), [
    [12n, 3],
    [11n, 1],
  ]);
});
