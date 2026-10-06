// Shared types and small helpers for backup, restore and verification.
// The CLI and TUI import these through index.ts.

import type { Repo } from '../core/repo.js';
import type { Manifest } from '../core/manifest.js';
import type { Snapshot, File, Tree } from '../core/snapshot.js';

export type { Snapshot, File, Tree };

// Running backup totals, reported after each file.
export interface Progress {
  path: string;
  files: number;
  bytes: number;
  newBytes: number;
  uploadedBytes: number;
}

export interface BackupOptions {
  paths: string[];
  exclude?: string[];
  dryRun?: boolean;
  host?: string;
  progress?: (p: Progress) => void;
}

interface ChangeCount {
  added: number;
  changed: number;
  removed: number;
}

// Differences from the last saved snapshot, counted separately for files and folders.
export interface Changes {
  files: ChangeCount;
  folders: ChangeCount;
}

// A file with new bytes to upload, listed by a dry run.
export interface PlannedFile {
  path: string;
  size: number;
  newBytes: number;
}

export interface BackupResult {
  snapshot: Snapshot;
  unchanged: boolean;
  changes: Changes;
  compared: boolean;
  planned: PlannedFile[];
}

// The outcome of the latest backup, kept in the manifest for `status` and the browser.
export interface LastRun {
  time: string;
  snapshot_id?: string;
  unchanged?: boolean;
  error?: string;
  skipped?: number;
  kept?: number;
  missing?: string[];
}

export interface VerifyResult {
  time: string;
  checked: number;
  total: number;
  failures?: string[];
}

// `checking` is true while a resumed or overwrite restore compares files that are already there.
export interface RestoreProgress {
  checking: boolean;
  path: string;
  files: number;
  totalFiles: number;
  bytes: number;
  totalBytes: number;
}

export interface RestoreOptions {
  target?: string;
  newTarget?: boolean;
  base?: string;
  include?: string[];
  progress?: (p: RestoreProgress) => void;
}

export interface RestoreResult {
  files: number;
  dirs: number;
  bytes: number;
  unfinished: boolean;
}

// What backup, restore and verification need from an engine. The manifest is optional because
// restore works without it, and the browser restores with a repository-only engine.
export interface EngineLike {
  repo: Repo;
  manifest?: Manifest;
  uploaders: number;
  downloaders: number;
  // Test hook that runs after each chunk of a source file is read.
  chunkRead?: (path: string) => void | Promise<void>;
}

// Throws the abort reason once the signal has fired.
export function check(signal?: AbortSignal): void {
  signal?.throwIfAborted();
}

export function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// The errno code of a Node.js system error, such as ENOENT.
export function code(e: unknown): string | undefined {
  return (e as NodeJS.ErrnoException)?.code;
}

export function emptyChanges(): Changes {
  return { files: { added: 0, changed: 0, removed: 0 }, folders: { added: 0, changed: 0, removed: 0 } };
}

export function changesNone(c: Changes): boolean {
  return Object.values(c.files).every(n => !n) && Object.values(c.folders).every(n => !n);
}
