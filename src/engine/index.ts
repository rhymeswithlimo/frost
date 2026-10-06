// The engine runs backup, restore and verification against one repository and, optionally, its manifest.
// The CLI and TUI use the Engine class; the work itself lives in backup.ts, restore.ts and verify.ts.

import type { Repo } from '../core/repo.js';
import type { Manifest } from '../core/manifest.js';
import { backup } from './backup.js';
import { restore } from './restore.js';
import { refreshSnapshots, verify } from './verify.js';
import type { BackupOptions, LastRun, RestoreOptions, VerifyResult } from './types.js';

export * from './types.js';
export * from './restore.js';
export { goneText } from './verify.js';

export class Engine {
  // Concurrent chunk uploads and downloads.
  uploaders = 4;
  downloaders = 8;

  // Test hook for backup reads, described in EngineLike.
  chunkRead?: (path: string) => void | Promise<void>;

  // Leave out the manifest for a repository-only engine, which can restore but not back up or verify.
  constructor(
    public repo: Repo,
    public manifest?: Manifest,
  ) {}

  backup(opts: BackupOptions, signal?: AbortSignal) {
    return backup(this, opts, signal);
  }

  restore(id: string, opts: RestoreOptions = {}, signal?: AbortSignal) {
    return restore(this, id, opts, signal);
  }

  verify(n: number, full = false, signal?: AbortSignal) {
    return verify(this, n, full, signal);
  }

  refreshSnapshots(signal?: AbortSignal) {
    return refreshSnapshots(this, signal);
  }

  lastBackup(): LastRun | undefined {
    return this.manifest?.getMeta<LastRun>('last_backup');
  }

  lastVerify(): VerifyResult | undefined {
    return this.manifest?.getMeta<VerifyResult>('verify');
  }

  // Verification is due when the last check is missing, failed, more than a day old or dated in the future.
  verifyDue(): boolean {
    const v = this.lastVerify();
    const age = v ? Date.now() - Date.parse(v.time) : Infinity;
    return !v || !!v.failures?.length || age < 0 || age > 24 * 3600_000;
  }
}
