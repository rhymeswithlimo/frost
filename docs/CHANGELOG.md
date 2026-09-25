# Changelog

Notable changes to frost are organised by release. Each release entry must use these exact three headings, in this precise order, with no extra headings included. If a release contains no fixes, omit the Fixes section:

1. **Added** - new features
2. **Fixed** - bug fixes
3. **Improved** - changes to existing behavior that aren't new features or fixes

Each bullet is 2-3 sentences: one bolded sentence stating what changed, in plain language, then one or two plain sentences explaining it simply.

## v0.1.0

### Added

- **Encrypted backups to S3-compatible storage.** frost backs up your directories to any S3-compatible bucket, and everything is encrypted on your machine before upload. Your key is shown once as a 24 word recovery phrase and never leaves the machine.

- **Permafrost as a second storage option.** You can pick hosted Permafrost storage instead of running your own bucket. It gets the same encrypted blobs as S3, and its HTTP API is documented in `docs/PERMAFROST.md`.

- **Incremental backups with content-defined chunking.** Files are split where their content says, not at fixed offsets, so an edit only re-uploads the chunk or two around it. Files that haven't changed since the last run aren't even read.

- **Snapshots with readable IDs.** Every backup is a snapshot with an ID like `maple-otter-3f1c`. You can restore by ID or by time, like `latest`, `3 days ago` or `2026-09-20`.

- **Dry runs.** `frost backup --dry-run` lists every file with new data and the total that would upload. Nothing is uploaded or saved.

- **Automatic backups without a daemon.** `frost init` installs a launchd, systemd, cron or Task Scheduler job depending on your OS. Changing the schedule with `frost config set` updates the job.

- **Automatic verification.** After each backup frost re-downloads a random sample of chunks and checks them against their hashes. `frost status` shows the result, and `frost status --verify` runs a check on demand.

- **Snapshot browser.** `frost browse` opens a terminal UI for browsing snapshots by date, walking the files as they were, comparing two snapshots and picking files to restore. Every shortcut is shown on screen, and the key fingerprint stays covered until you press `[v]`.

- **Key management.** `frost key` shows your recovery phrase behind a confirmation, checks a phrase against your backups, or imports one on a new machine.

- **One-line install.** `install/install.sh` detects macOS, Linux, WSL or Git Bash, downloads the right binary and checks its SHA-256 before installing it.

### Improved

- **Restores never overwrite by default.** Files go into a new `frost-restore-<id>` folder unless you ask for `--in-place`. Each file is written to a temp file first, so a failed restore can't leave a half-written file behind.

- **Interrupted backups resume cheaply.** Uploaded chunks are recorded as the run goes, not just at the end. If a backup is cut off, the next run skips everything that already made it up.

- **A new machine doesn't re-upload everything.** If the local manifest is missing, frost rebuilds it from the chunks already in storage. Only data that's actually new gets uploaded.

- **Unreadable folders can't hide behind a green "ok".** A backed-up directory that can't be read fails the backup, and skipped items inside it show up in `frost status`. On macOS the error explains how to grant Full Disk Access.

- **The browser doesn't block scheduled backups.** It reads what it needs from the local manifest up front and releases the lock. A scheduled backup can run while the browser is open.

- **The browser fits small terminals.** Every screen is clipped to the window and switches to a compact layout when space is tight. The wordmark steps aside when there isn't room for it.
