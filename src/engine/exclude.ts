// Exclude patterns for backup. A pattern without a slash is tested against each file or folder name.
// A pattern with a slash is tested against the full path and each of its parent folders.

import path from 'node:path';

// Snapshots store paths with forward slashes on every platform.
export function slash(p: string): string {
  return process.platform === 'win32' ? p.replaceAll('\\', '/') : p;
}

// Turns a glob into an anchored regex. `*` and `?` never match a slash, `[...]` is a character class
// (`[^...]` negates it) and a backslash escapes the next character.
export function patternRegex(pattern: string): RegExp {
  let out = '^';
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i];
    if (c === '*') out += '[^/]*';
    else if (c === '?') out += '[^/]';
    else if (c === '[') {
      const end = pattern.indexOf(']', i + 1);
      if (end < 0 || end === i + 1) throw new Error('syntax error in pattern');
      out += '[' + pattern.slice(i + 1, end) + ']';
      i = end;
    } else if (c === '\\') {
      if (++i === pattern.length) throw new Error('syntax error in pattern');
      out += pattern[i].replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    } else out += c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  // A class the regex engine rejects becomes the same error as any other bad pattern.
  try {
    return new RegExp(out + '$', 'u');
  } catch {
    throw new Error('syntax error in pattern');
  }
}

export class Excluder {
  private names: RegExp[] = [];
  private paths: RegExp[] = [];

  constructor(patterns: string[]) {
    for (let p of patterns) {
      p = slash(p).trim().replace(/\/$/, '');
      if (!p) continue;
      (p.includes('/') ? this.paths : this.names).push(patternRegex(p));
    }
  }

  match(p: string): boolean {
    if (!this.names.length && !this.paths.length) return false;
    p = slash(p);
    if (this.names.some(n => n.test(path.posix.basename(p)))) return true;

    // Test the path itself, then each parent folder up to the root.
    for (const regex of this.paths)
      for (let q = p; q !== '/' && q !== '.' && q; q = path.posix.dirname(q)) {
        if (regex.test(q)) return true;
        if (path.posix.dirname(q) === q) break;
      }
    return false;
  }
}
