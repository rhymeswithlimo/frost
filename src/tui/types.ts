// These are the TUI's shared types. Most are re-exported from core so the TUI has one place to import them from.
// The CLI and the demo build a BrowserRepo and State from a real or fake repository and hand them to the browser.

import type { Snapshot, Tree } from '../core/snapshot.js';
export type { Snapshot, Stats, FileEntry, Tree, Change } from '../core/snapshot.js';
export type { Storage, Config } from '../core/config.js';

export interface Key {
  phrase(): string;
  fingerprint(): string;
}

export interface LastRun {
  time: string;
  error?: string;
  skipped?: number;
  kept?: number;
}

export interface VerifyResult {
  time: string;
  checked: number;
  failures?: unknown[];
}

// State holds what the CLI already knows when the browser opens, so the home screen can show it straight away.
// `known` doubles as a snapshot cache, so the repository only fetches snapshots it hasn't seen.
export interface State {
  known?: Map<string, Snapshot> | Record<string, Snapshot>;
  last?: LastRun;
  hasLast?: boolean;
  verify?: VerifyResult;
  hasVerify?: boolean;
  version?: string;
  updates?: string;
}

export interface RestoreResult {
  files: number;
  dirs?: number;
  bytes: number;
  unfinished?: boolean;
}

export interface RestoreOptions {
  target?: string;
  newTarget?: boolean;
  base?: string;
  include: string[];
  progress?: (p: { files: number; totalFiles: number; path: string }) => void;
}

// BrowserRepo is the narrow repository surface the browser needs. The CLI adapts the real repository to it
// and tests fake it.
export interface BrowserRepo {
  label: string;
  fingerprint: string;
  snapshots(known?: State['known'], signal?: AbortSignal): Promise<Snapshot[]>;
  loadTree(id: string, signal?: AbortSignal): Promise<Tree>;
  restore(id: string, opts: RestoreOptions, signal?: AbortSignal): Promise<RestoreResult>;
}

export { defaultConfig } from '../core/config.js';
