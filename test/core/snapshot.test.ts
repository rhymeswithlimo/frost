// Tests for snapshot IDs, selectors and time parsing, safe restore paths, restore bases and
// tree diffs.

import test from 'node:test';
import assert from 'node:assert/strict';
import {
  newID,
  validID,
  short,
  shorten,
  resolve,
  parseTime,
  safeRel,
  commonDir,
  restoreBase,
  restoreRel,
  isRoot,
  diff,
  compare,
  emptyStats,
  type Snapshot,
  type FileEntry,
} from '../../src/core/snapshot.js';

const header = (id: string, time: string): Snapshot => ({ id, time, host: 'test', paths: [], stats: emptyStats() });

test('readable IDs and collision-aware shortening', () => {
  const id = newID();
  assert.ok(validID(id));
  assert.notEqual(newID(), id);

  // Short IDs keep four hex digits, and shorten adds digits only where two IDs would clash.
  assert.equal(short(id).length, id.length - 7);
  const ids = [
    'maple-absurd-3f1c9a0b2e7',
    'maple-absurd-3f1c0000000',
    'maple-acid-3f1c9a0b2e7',
    'old-style-0001',
    'old-style-00011',
  ];
  const snapshots = ids.map(id => header(id, '2026-10-04T00:00:00Z'));
  const shortened = shorten(snapshots);
  assert.equal(shortened.get(ids[0]), 'maple-absurd-3f1c9');
  assert.equal(shortened.get(ids[1]), 'maple-absurd-3f1c0');
  for (const [id, prefix] of shortened) assert.equal(resolve(snapshots, prefix).id, id);
  for (const value of ['', '../x', 'upper-X', 'a'.repeat(65)]) assert.ok(!validID(value));
});

// An exact ID beats a prefix match, and duplicate IDs resolve to the newest copy. Times compare
// at full nanosecond precision.
test('selectors preserve exact-ID precedence, input order and timestamp precision', () => {
  const now = new Date('2026-10-04T12:00:00Z');
  const snapshots = [
    header('apple', '2026-10-02T12:00:00Z'),
    header('apple-long', '2026-10-04T11:00:00Z'),
    header('older', '2026-10-01T12:00:00Z'),
    header('apple', '2026-10-03T12:00:00Z'),
  ];
  assert.equal(resolve(snapshots, 'latest', now), snapshots[1]);
  assert.equal(resolve(snapshots, 'apple', now), snapshots[3]);
  assert.equal(resolve(snapshots, '2 days ago', now), snapshots[0]);
  assert.throws(() => resolve(snapshots, 'app', now), /matches 3/);
  assert.throws(() => resolve([], 'latest', now), /no snapshots/);
  assert.throws(() => resolve(snapshots, '10 days ago', now), /oldest is/);

  const precise = [header('a', '2026-10-04T12:00:00.123456788Z'), header('b', '2026-10-04T12:00:00.123456789Z')];
  assert.equal(resolve(precise).id, 'b');
  for (const value of ['999999999999999999999 years ago', '100000 weeks ago', '2026-02-30', 'nonsense'])
    assert.throws(() => parseTime(value, now));
});

// A bare date means the end of that day in local time. Years below 100 must not be read as 19xx.
test('absolute calendar selectors keep four-digit years and Gregorian leap validation', () => {
  const now = new Date('2026-10-05T12:00:00Z');
  for (const [value, year, month, day] of [
    ['0000-02-29', 0, 1, 29],
    ['0004-02-29', 4, 1, 29],
    ['0099-01-01', 99, 0, 1],
    ['2000-02-29', 2000, 1, 29],
  ] as const) {
    const time = parseTime(value, now);
    assert.equal(time.getFullYear(), year);
    assert.equal(time.getMonth(), month);
    assert.equal(time.getDate(), day);
    assert.equal(time.getHours(), 23);
    assert.equal(time.getMinutes(), 59);
  }
  for (const value of ['0001-02-29', '1900-02-29', '0099-01-01 24:00']) assert.throws(() => parseTime(value, now));
  assert.throws(() => resolve([header('old', '0004-02-29T12:00:00Z')], '0001-01-01', now), /oldest is 0004-/);
});

test('safe restore paths reject Windows aliases and traversal on both platforms', () => {
  for (const [input, expected] of [
    ['/home/me/a.txt', 'home/me/a.txt'],
    ['C:/Users/me/a', 'C/Users/me/a'],
    ['/a/./b', 'a/b'],
  ])
    assert.equal(safeRel(input, 'win32'), expected);
  for (const platform of ['linux', 'win32'] as const)
    for (const value of ['/../etc/passwd', '/a/../../b', '/', '', 'a/../..', '/x/\0'])
      assert.throws(() => safeRel(value, platform));

  // Device names, alternate data streams, trailing dots and spaces, and backslash traversal are
  // only dangerous on Windows.
  for (const value of [
    'C:/x/..\\..\\evil',
    '/NUL',
    '/COM1',
    '/x/file:stream',
    '/x/trailing.',
    '/x/trailing ',
    '/CON.txt',
    '/CONIN$',
    '/CONOUT$',
  ])
    assert.throws(() => safeRel(value, 'win32'));
  assert.equal(safeRel('/:file', 'linux'), ':file');
  assert.equal(safeRel('/a/..\\b', 'linux'), 'a/..\\b');
  assert.equal(safeRel('/a/'), 'a');
});

// The restore base is the parent of the selection, so a restored folder keeps its own name.
test('restore common parent and relative targets preserve selected names', () => {
  assert.equal(commonDir(['/home/me/docs/a', '/home/me/pics/b']), '/home/me');
  assert.equal(commonDir(['C:/a', 'D:/b']), '');
  assert.equal(commonDir(['C:/a', 'C:/b']), 'C:/');
  assert.equal(restoreBase(['/home/me/docs', '/home/me/docs/a']), '/home/me');
  assert.equal(restoreBase(['C:/docs']), 'C:/');
  assert.equal(restoreRel('/home/me/docs/a', '/home/me'), 'docs/a');
  assert.equal(restoreRel('C:/a', ''), 'C/a');
  for (const value of ['/home/me', '/home', '/home/meta/x', '/home/me/../x'])
    assert.throws(() => restoreRel(value, '/home/me'));
  assert.ok(isRoot('/'));
  assert.ok(isRoot('C:/'));
  assert.ok(!isRoot('/home'));
});

test('diff ignores modification time and returns references in sorted and unsorted trees', () => {
  const file = (path: string, size = 0, mtime = '2026-10-04T12:00:00Z'): FileEntry => ({
    path,
    type: 'file',
    mode: 0o600,
    size,
    mtime,
  });

  // `/touched` differs only in mtime, so it isn't reported.
  const before = { files: [file('/a'), file('/c', 1), file('/d'), file('/touched')] };
  const after = { files: [file('/b'), file('/c', 2), file('/e'), file('/touched', 0, '2026-10-04T13:00:00Z')] };
  const changes = diff(before, after);
  assert.deepEqual(
    changes.map(c => [c.path, c.kind]),
    [
      ['/a', 'removed'],
      ['/b', 'added'],
      ['/c', 'modified'],
      ['/d', 'removed'],
      ['/e', 'added'],
    ],
  );
  assert.equal(changes[2].old, before.files[1]);
  assert.deepEqual(diff({ files: [...before.files].reverse() }, after), changes);

  // Paths sort by UTF-8 bytes. UTF-16 order would put the emoji before U+E000.
  const names = ['/a', '/\ue000', '/😀', '/😁'];
  assert.deepEqual(
    [...names].sort(compare),
    [...names].sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b))),
  );
});
