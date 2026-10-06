// Compares a backup's file list with the last saved one. The full SHA-256 digest decides whether
// anything changed. Per-entry hashes only count additions, changes and removals for output.

import { createHash } from 'node:crypto';
import type { Changes, Tree } from './types.js';
import { emptyChanges } from './types.js';
import { marshal } from '../core/repo.js';

// Set in a record's path hash when the entry is a folder.
const top = 1n << 63n;

// 64-bit FNV-1a, computed in two 32-bit halves so it runs on plain numbers instead of BigInt.
function fnv(data: Buffer): bigint {
  let low = 0x84222325;
  let high = 0xcbf29ce4;
  for (const b of data) {
    // The FNV prime is 2^40 + 435. Its 2^40 term only reaches the high half, as low * 256.
    const previous = (low ^ b) >>> 0;
    const product = previous * 435;
    low = product >>> 0;
    high = (high * 435 + previous * 256 + Math.floor(product / 4294967296)) >>> 0;
  }
  return (BigInt(high) << 32n) | BigInt(low);
}

// Hashes the backup paths and every entry into one SHA-256 digest. `entries` holds a 16-byte record per
// entry (path hash, then entry hash), sorted by path hash, and is only built when it's read.
export function listDigest(paths: string[], tree: Tree): { digest: string; entries: string } {
  const hash = createHash('sha256');
  for (const p of paths) hash.update(p).update('\0');
  hash.update(Buffer.from([1]));

  // The fields match the stored file list, except that a folder's modification time is left out.
  const rawFiles: { path: string; directory: boolean; raw: Buffer }[] = [];
  for (const f of tree.files) {
    const raw = marshal({
      path: f.path,
      type: f.type,
      mode: f.mode,
      mtime: f.type === 'dir' ? '0001-01-01T00:00:00Z' : f.mtime,
      ...(f.size ? { size: f.size } : {}),
      ...(f.chunks?.length ? { chunks: f.chunks } : {}),
      ...(f.target ? { target: f.target } : {}),
    });
    hash.update(raw).update('\n');
    rawFiles.push({ path: f.path, directory: f.type === 'dir', raw });
  }

  let computed: string | undefined;
  return {
    digest: hash.digest('base64'),
    get entries() {
      if (computed !== undefined) return computed;
      const list = rawFiles.map(f => ({
        key: (fnv(Buffer.from(f.path)) & ~top) | (f.directory ? top : 0n),
        sum: fnv(f.raw),
      }));
      list.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
      const entries = Buffer.alloc(list.length * 16);
      list.forEach((e, i) => {
        entries.writeBigUInt64BE(e.key, i * 16);
        entries.writeBigUInt64BE(e.sum, i * 16 + 8);
      });
      return (computed = entries.toString('base64'));
    },
  };
}

// Merges two sorted record lists from listDigest. A path hash in only one list is an addition or
// removal; one in both with different entry hashes is a change.
export function countChanges(was: string, now: string): Changes {
  if (was === now) return emptyChanges();
  const a = Buffer.from(was, 'base64');
  const b = Buffer.from(now, 'base64');
  const c = emptyChanges();
  let i = 0;
  let j = 0;
  const count = (key: bigint) => (key & top ? c.folders : c.files);

  while (i + 16 <= a.length || j + 16 <= b.length) {
    if (j + 16 > b.length) {
      count(a.readBigUInt64BE(i)).removed++;
      i += 16;
    } else if (i + 16 > a.length) {
      count(b.readBigUInt64BE(j)).added++;
      j += 16;
    } else {
      const ak = a.readBigUInt64BE(i);
      const bk = b.readBigUInt64BE(j);
      if (ak < bk) {
        count(ak).removed++;
        i += 16;
      } else if (ak > bk) {
        count(bk).added++;
        j += 16;
      } else {
        if (a.readBigUInt64BE(i + 8) !== b.readBigUInt64BE(j + 8)) count(bk).changed++;
        i += 16;
        j += 16;
      }
    }
  }
  return c;
}
