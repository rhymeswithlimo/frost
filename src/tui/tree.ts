// Turns a snapshot's flat file list into the folder tree the browser walks, with recursive totals for
// folder sizes and the selection summary.

import path from 'node:path';
import type { Snapshot, Tree, FileEntry } from './types.js';
import { compare } from '../core/snapshot.js';

// The snapshot's backed-up paths sit under this key, as the browser's top level.
const rootKey = '';

// Reports whether p, or any folder above it, is selected.
export function covered(p: string, sel: Set<string>): boolean {
  for (let q = p; ; q = path.posix.dirname(q)) {
    if (sel.has(q)) return true;
    if (path.posix.dirname(q) === q) return false;
  }
}

export class FileTree {
  // These map every entry by path, each folder to its sorted children, and each folder to its recursive
  // file count and size.
  files = new Map<string, FileEntry>();
  children = new Map<string, string[]>();
  totals = new Map<string, { files: number; bytes: number }>();
  roots: string[] = [];

  constructor(s: Snapshot, t: Tree) {
    for (const f of t.files) this.files.set(f.path, f);
    const roots = new Set(s.paths.filter(p => this.files.has(p)));
    this.roots = [...roots];

    // File each entry under its parent, with roots under rootKey. Each file's size is added to every
    // folder above it, stopping at its root or the top of the drive.
    for (const [p, f] of this.files) {
      const parent = roots.has(p) ? rootKey : path.posix.dirname(p);
      const kids = this.children.get(parent) ?? [];
      kids.push(p);
      this.children.set(parent, kids);
      if (f.type === 'file' && !roots.has(p)) {
        for (let q = path.posix.dirname(p); ; q = path.posix.dirname(q)) {
          const total = this.totals.get(q) ?? { files: 0, bytes: 0 };
          total.files++;
          total.bytes += f.size ?? 0;
          this.totals.set(q, total);
          if (roots.has(q) || path.posix.dirname(q) === q) break;
        }
      }
    }

    // Children sort folders first, then by name ignoring case, then by full path. Names lowercase one
    // character at a time, and İ becomes a plain i rather than i plus a combining dot.
    for (const kids of this.children.values()) {
      const names = new Map(
        kids.map(p => [p, [...path.posix.basename(p)].map(r => (r === 'İ' ? 'i' : r.toLowerCase())).join('')]),
      );
      kids.sort(
        (a, b) =>
          Number(this.isDir(b)) - Number(this.isDir(a)) || compare(names.get(a)!, names.get(b)!) || compare(a, b),
      );
    }
  }

  isDir(p: string): boolean {
    return this.files.get(p)?.type === 'dir';
  }

  // Returns the folder one level up in the browser. Roots go back to the top level.
  parent(dir: string): string {
    return this.roots.includes(dir) || !dir ? rootKey : path.posix.dirname(dir);
  }

  // Counts the files and bytes a restore of the selection would write. Paths inside a selected folder
  // are skipped because the folder's total already counts them.
  selectionTotals(sel: Set<string>): { files: number; bytes: number } {
    let files = 0;
    let bytes = 0;
    for (const p of sel) {
      const parent = path.posix.dirname(p);
      if (parent !== p && covered(parent, sel)) continue;
      const f = this.files.get(p);
      const t = this.totals.get(p) ?? {
        files: f?.type === 'file' ? 1 : 0,
        bytes: f?.type === 'file' ? (f.size ?? 0) : 0,
      };
      files += t.files;
      bytes += t.bytes;
    }
    return { files, bytes };
  }
}
