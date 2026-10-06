// Extracts release archives in memory, gzipped tar for macOS and Linux and zip for Windows.
// Only regular files with safe, unique names come out, and the expanded size is capped.
import { gunzipSync, inflateRawSync } from 'node:zlib';

export interface ArchiveEntry {
  name: string;
  data: Buffer;
  mode: number;
}

const maxExpanded = 384 * 1024 * 1024;

// Returns the name without a leading "./", or throws. Refuses absolute and drive paths,
// backslashes, empty and dot components, characters or trailing dots and spaces that Windows
// can't store, and Windows device names, including the superscript-digit COM and LPT forms.
export function safeArchiveName(name: string): string {
  if (!name || name.includes('\\') || name.includes('\0') || name.startsWith('/') || /^[A-Za-z]:/.test(name))
    throw new Error('unsafe archive path');
  const clean = name.replace(/^\.\//, '');
  if (
    clean
      .split('/')
      .some(
        p =>
          p === '..' ||
          p === '.' ||
          !p ||
          /[\x00-\x1f:*?"<>|]/.test(p) ||
          /[. ]$/.test(p) ||
          /^(con|prn|aux|nul|conin\$|conout\$|com[1-9¹²³]|lpt[1-9¹²³])(?:\.|$)/i.test(p),
      )
  )
    throw new Error('unsafe archive path');
  return clean;
}

// Duplicates are compared case-insensitively, since Windows and macOS folders usually are.
// Only permission bits survive from the mode.
function checkEntry(
  name: string,
  data: Buffer,
  mode: number,
  seen: Set<string>,
  total: { value: number },
): ArchiveEntry {
  const clean = safeArchiveName(name);
  const folded = clean.toLowerCase();
  if (seen.has(folded)) throw new Error('duplicate archive path');
  seen.add(folded);
  total.value += data.length;
  if (total.value > maxExpanded) throw new Error('archive too big');
  return { name: clean, data, mode: mode & 0o777 };
}

// Picks the format from the archive's file name.
export function extractArchive(name: string, archive: Buffer): ArchiveEntry[] {
  const seen = new Set<string>();
  const total = { value: 0 };
  return name.endsWith('.zip') ? zipEntries(archive, seen, total) : tarEntries(archive, seen, total);
}

// Reads ustar headers: name at 0, mode at 100, size at 124 (octal), checksum at 148, type at 156
// and prefix at 345. Regular files ("0" or NUL) are kept and folders ("5") are only name-checked.
// Links, devices and extended headers are refused. A zero block ends the archive.
function tarEntries(archive: Buffer, seen: Set<string>, total: { value: number }): ArchiveEntry[] {
  const b = gunzipSync(archive, { maxOutputLength: maxExpanded });
  const entries: ArchiveEntry[] = [];
  let off = 0;
  const str = (start: number, len: number) =>
    b
      .subarray(start, start + len)
      .toString()
      .replace(/\0.*$/s, '');

  while (off + 512 <= b.length) {
    if (b.subarray(off, off + 512).every(n => n === 0)) return entries;
    const type = b[off + 156];
    const entry = str(off, 100);
    const prefix = str(off + 345, 155);
    const size = parseInt(str(off + 124, 12).trim(), 8);
    const mode = parseInt(str(off + 100, 8).trim(), 8);
    if (!Number.isSafeInteger(size) || size < 0 || size > maxExpanded || off + 512 + size > b.length)
      throw new Error('truncated archive');

    // The header checksum sums every header byte, counting its own 8-byte field as spaces.
    const checksum = parseInt(str(off + 148, 8).trim(), 8);
    let actual = 0;
    for (let i = 0; i < 512; i++) actual += i >= 148 && i < 156 ? 32 : b[off + i];
    if (checksum !== actual) throw new Error('bad tar checksum');

    const name = prefix ? prefix + '/' + entry : entry;
    if (type === 48 || type === 0)
      entries.push(checkEntry(name, Buffer.from(b.subarray(off + 512, off + 512 + size)), mode, seen, total));
    else if (type === 53) {
      if (name !== './' && name !== '.') safeArchiveName(name.replace(/\/$/, ''));
    } else throw new Error('archive contains unsupported entry');

    // File data is padded to whole 512-byte blocks.
    off += 512 + Math.ceil(size / 512) * 512;
  }
  throw new Error('truncated archive');
}

// The standard CRC-32 (reflected polynomial 0xedb88320) that zip uses.
const crcTable = Array.from({ length: 256 }, (_, n) => {
  for (let i = 0; i < 8; i++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1;
  return n >>> 0;
});

export function crc32(data: Buffer): number {
  let crc = 0xffffffff;
  for (const b of data) crc = crcTable[(crc ^ b) & 255] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

// Reads a single-disk zip without zip64 or encryption, stored or deflated. Each local header
// must agree with its central directory entry, and the central directory must fill the space
// up to the end record exactly.
function zipEntries(b: Buffer, seen: Set<string>, total: { value: number }): ArchiveEntry[] {
  // The end of central directory record (PK\5\6) is 22 bytes plus a comment of up to 65535
  // bytes, and the comment must reach the end of the file.
  let end = -1;
  for (let off = b.length - 22; off >= Math.max(0, b.length - 65557); off--) {
    if (b.readUInt32LE(off) === 0x06054b50 && off + 22 + b.readUInt16LE(off + 20) === b.length) {
      end = off;
      break;
    }
  }
  if (end < 0 || b.readUInt16LE(end + 4) || b.readUInt16LE(end + 6)) throw new Error('invalid zip archive');
  const count = b.readUInt16LE(end + 10);
  const cdSize = b.readUInt32LE(end + 12);
  const cdOff = b.readUInt32LE(end + 16);
  if (count === 65535 || cdOff + cdSize !== end) throw new Error('invalid zip directory');

  const entries: ArchiveEntry[] = [];
  let off = cdOff;
  for (let i = 0; i < count; i++) {
    // Central directory entry (PK\1\2): flags at 8, method at 10, CRC at 16, sizes at 20 and
    // 24, name, extra and comment lengths at 28 to 32, external attributes at 38, and the local
    // header offset at 42. The name starts at 46.
    if (off + 46 > end || b.readUInt32LE(off) !== 0x02014b50) throw new Error('invalid zip directory');
    const flags = b.readUInt16LE(off + 8);
    const method = b.readUInt16LE(off + 10);
    const crc = b.readUInt32LE(off + 16);
    const size = b.readUInt32LE(off + 24);
    const compressed = b.readUInt32LE(off + 20);
    const len = b.readUInt16LE(off + 28);
    const extra = b.readUInt16LE(off + 30);
    const comment = b.readUInt16LE(off + 32);
    const attrs = b.readUInt32LE(off + 38);
    const local = b.readUInt32LE(off + 42);
    if (
      flags & 1 ||
      ![0, 8].includes(method) ||
      size > maxExpanded - total.value ||
      off + 46 + len + extra + comment > end ||
      local + 30 > cdOff
    )
      throw new Error('unsupported zip entry');
    const nameBytes = b.subarray(off + 46, off + 46 + len);
    const name = nameBytes.toString('utf8');

    // Local header (PK\3\4): method at 8, name and extra lengths at 26 and 28, data after them.
    if (b.readUInt32LE(local) !== 0x04034b50 || b.readUInt16LE(local + 8) !== method)
      throw new Error('invalid zip header');
    const localLen = b.readUInt16LE(local + 26);
    const localExtra = b.readUInt16LE(local + 28);
    const begin = local + 30 + localLen + localExtra;
    if (begin + compressed > cdOff || !b.subarray(local + 30, local + 30 + localLen).equals(nameBytes))
      throw new Error('invalid zip header');

    // The top 16 bits of the external attributes hold a Unix mode when the zip has one. Only
    // regular files and folders are allowed.
    const fileMode = attrs >>> 16;
    if (fileMode && (fileMode & 0o170000) !== 0o100000 && (fileMode & 0o170000) !== 0o040000)
      throw new Error('archive contains unsupported entry');

    // Folders are only name-checked. Files inflate no further than the declared size, then the
    // size and CRC must match.
    if (name.endsWith('/')) safeArchiveName(name.slice(0, -1));
    else {
      const data =
        method === 0
          ? Buffer.from(b.subarray(begin, begin + compressed))
          : inflateRawSync(b.subarray(begin, begin + compressed), { maxOutputLength: Math.max(1, size) });
      if (data.length !== size || crc32(data) !== crc) throw new Error('zip checksum mismatch');
      entries.push(checkEntry(name, data, fileMode || 0o644, seen, total));
    }
    off += 46 + len + extra + comment;
  }
  if (off !== end) throw new Error('invalid zip directory');
  return entries;
}
