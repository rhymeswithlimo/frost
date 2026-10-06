// FastCDC content-defined chunking. Cut points come from a 64-bit gear hash whose table is
// seeded by the key, so known files don't have a public pattern of cut points. Chunks are
// 256 KiB to 8 MiB and average 1 MiB.

import { createHash } from 'node:crypto';
import { gearScan } from './gear.js';

export const minSize = 256 << 10;
export const avgSize = 1 << 20;
export const maxSize = 8 << 20;

// The gear table. Each 64-bit entry is split into 32-bit halves, since JavaScript bit
// operations work on 32 bits.
export interface Table {
  low: Uint32Array;
  high: Uint32Array;
}

// Recently built tables by seed. Callers always get copies, so they can't change the cache.
const tables = new Map<bigint, Table>();

// Scratch state for the fast path over a run of one repeated byte, such as zero-filled space.
const uniformProbe = Buffer.alloc(64);
let uniform = Buffer.alloc(0),
  uniformByte = -1;

// Builds the gear table for a seed. Entry i is the first 8 bytes of SHA-256 over the seed then i,
// each written as a little-endian 64-bit integer.
export function newTable(seed: bigint): Table {
  seed = BigInt.asUintN(64, seed);
  const previous = tables.get(seed);
  if (previous) return { low: previous.low.slice(), high: previous.high.slice() };

  const low = new Uint32Array(256),
    high = new Uint32Array(256),
    input = Buffer.alloc(16);
  input.writeBigUInt64LE(BigInt.asUintN(64, seed));
  for (let i = 0; i < 256; i++) {
    input.writeBigUInt64LE(BigInt(i), 8);
    const hash = createHash('sha256').update(input).digest();
    low[i] = hash.readUInt32LE();
    high[i] = hash.readUInt32LE(4);
  }

  // Keep at most 32 seeds, dropping the oldest.
  tables.set(seed, { low: low.slice(), high: high.slice() });
  if (tables.size > 32) tables.delete(tables.keys().next().value!);
  return { low, high };
}

// Returns the length of the first chunk in data. The hash is h = (h << 1) + gear[byte] mod 2^64,
// and a cut falls where its masked top bits are zero. Scanning starts at 256 KiB, the mask checks
// the top 22 bits before 1 MiB and the top 18 after, and 8 MiB always cuts. The scan itself runs in
// WebAssembly (see gear.ts).
export function cutpoint(data: Buffer, table: Table): number {
  const n = Math.min(data.length, maxSize);
  if (n <= minSize) return n;
  const normal = Math.min(n, avgSize),
    lowTable = table.low,
    highTable = table.high;
  let low = 0,
    high = 0;

  // When everything after minSize is one repeated byte, the hash settles within 64 bytes, so
  // the answer follows without scanning megabytes.
  const byte = data[minSize];
  uniformProbe.fill(byte);
  if (n > minSize + 64 && data.subarray(minSize, minSize + 64).equals(uniformProbe)) {
    if (!uniform.length) uniform = Buffer.allocUnsafe(maxSize);
    if (uniformByte !== byte) {
      uniform.fill(byte);
      uniformByte = byte;
    }
    if (data.subarray(minSize, n).equals(uniform.subarray(0, n - minSize))) {
      for (let i = minSize; i < minSize + 64; i++) {
        const addition = low * 2 + lowTable[byte];
        high = (high * 2 + highTable[byte] + Math.floor(addition / 4294967296)) >>> 0;
        low = addition >>> 0;
        if ((high & (i < normal ? 0xfffffc00 : 0xffffc000)) === 0) return i + 1;
      }
      // After 64 identical bytes, the rolling hash stays equal to minus that byte's gear value.
      return normal < n && (high & 0xffffc000) === 0 ? normal + 1 : n;
    }
  }

  return gearScan(data, minSize, normal, n, table) ?? scanScalar(data, minSize, normal, n, table);
}

// The same scan in plain JavaScript, used only when WebAssembly isn't available. The high and
// low halves stand in for one 64-bit hash, with the carry from the low half added by hand.
function scanScalar(data: Buffer, start: number, normal: number, end: number, table: Table): number {
  let low = 0;
  let high = 0;
  for (let i = start; i < end; i++) {
    const addition = low * 2 + table.low[data[i]];
    high = (high * 2 + table.high[data[i]] + Math.floor(addition / 4294967296)) >>> 0;
    low = addition >>> 0;
    if ((high & (i < normal ? 0xfffffc00 : 0xffffc000)) === 0) return i + 1;
  }
  return end;
}

// cutpoint without WebAssembly, so tests can check the fallback against the same vectors.
export function scalarCutpoint(data: Buffer, table: Table): number {
  const n = Math.min(data.length, maxSize);
  if (n <= minSize) return n;
  return scanScalar(data, minSize, Math.min(n, avgSize), n, table);
}

// Splits a whole buffer into chunks. The pieces are views of data, not copies.
export function split(data: Buffer, table: Table): Buffer[] {
  const result: Buffer[] = [];
  while (data.length) {
    const n = cutpoint(data, table);
    result.push(data.subarray(0, n));
    data = data.subarray(n);
  }
  return result;
}

// Chunks an async byte stream into copies. Input collects in a buffer of up to 16 MiB, and cuts
// happen only while at least 8 MiB is buffered or at the end, so the read sizes never change
// where chunks fall.
export async function* chunks(input: AsyncIterable<Uint8Array>, table: Table): AsyncGenerator<Buffer> {
  let buffered = Buffer.alloc(0);
  let start = 0;
  let end = 0;
  let empty = 0;

  for await (const bytes of input) {
    // Stop after 100 empty reads in a row instead of spinning.
    if (!bytes.length) {
      if (++empty >= 100) throw new Error('multiple Read calls return no data or error');
      continue;
    }
    empty = 0;

    let offset = 0;
    while (offset < bytes.length) {
      // Move unread bytes to the front, and grow a full buffer up to 16 MiB.
      if (start > 0) {
        buffered.copyWithin(0, start, end);
        end -= start;
        start = 0;
      }
      if (end === buffered.length) {
        const expanded = Buffer.allocUnsafe(
          Math.min(maxSize * 2, Math.max(Math.min(bytes.length - offset, maxSize), 8192, buffered.length * 2)),
        );
        buffered.copy(expanded, 0, 0, end);
        buffered = expanded;
      }

      const n = Math.min(bytes.length - offset, buffered.length - end);
      buffered.set(bytes.subarray(offset, offset + n), end);
      end += n;
      offset += n;

      if (end === maxSize * 2) {
        while (end - start >= maxSize) {
          const size = cutpoint(buffered.subarray(start, end), table);
          yield Buffer.from(buffered.subarray(start, start + size));
          start += size;
        }
      }
    }
  }

  // End of input: cut whatever is left.
  while (start < end) {
    const size = cutpoint(buffered.subarray(start, end), table);
    yield Buffer.from(buffered.subarray(start, start + size));
    start += size;
  }
  buffered = Buffer.alloc(0);
}

// The part of a file handle that chunksFromReader uses.
export interface Reader {
  read(buffer: Buffer, offset: number, length: number, position: null): Promise<{ bytesRead: number }>;
}

// Chunks a file through one reusable 8 to 16 MiB buffer, yielding copies. It refills whenever
// less than 8 MiB is buffered, so cuts match chunks() for the same bytes.
export async function* chunksFromReader(
  reader: Reader,
  table: Table,
  buffer = Buffer.allocUnsafe(maxSize * 2),
): AsyncGenerator<Buffer> {
  if (buffer.length < maxSize || buffer.length > maxSize * 2)
    throw new Error('chunk reader buffer must be between 8 MiB and 16 MiB');
  let start = 0;
  let end = 0;
  let eof = false;

  for (;;) {
    if (!eof && end - start < maxSize) {
      if (start) {
        buffer.copyWithin(0, start, end);
        end -= start;
        start = 0;
      }
      while (end < buffer.length) {
        const length = buffer.length - end;
        const { bytesRead } = await reader.read(buffer, end, length, null);
        if (!Number.isInteger(bytesRead) || bytesRead < 0 || bytesRead > length)
          throw new Error('chunk reader returned an invalid byte count');
        if (!bytesRead) {
          eof = true;
          break;
        }
        end += bytesRead;
      }
    }

    if (start === end) return;
    const size = cutpoint(buffer.subarray(start, end), table);
    yield Buffer.from(buffer.subarray(start, start + size));
    start += size;
  }
}
