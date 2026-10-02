# Changelog

Notable changes to frost, newest release first. Each release lists what was added, fixed and improved, in that order, and leaves out empty sections.

## v0.1.0

### Added

- **frost backs up to S3-compatible storage.** Everything is encrypted on your machine first, and kept in a `frost/` folder inside the bucket so the bucket can hold other things too.

- **frost can store backups on Permafrost.** Connecting takes one access key, with no bucket, region or endpoint to set up. You can also point frost at your own Permafrost server.

- **`frost init` can get you a Permafrost key.** If you don't have one yet, it opens a page in your browser, and the key comes back to frost and is saved straight away. If Permafrost rejects a key later, every command says so and points you to `frost init`.

- **`frost init` walks you through setup.** In a terminal it opens a full-screen setup that asks one thing at a time and says where to find each answer, with presets for Backblaze B2, Amazon S3, Cloudflare R2 and Wasabi. If connecting fails, it says why in plain words and goes back to the answer that caused it.

- **Backups only upload what changed.** Files are split where their content says, not at fixed offsets, so an edit only re-uploads the chunk or two around it. Files that haven't changed since the last run aren't even read.

- **Interrupted backups pick up where they stopped.** Uploaded chunks are recorded as the run goes, so the next run skips them. On a new machine, frost rebuilds its record from what's already in storage and only uploads new data.

- **Backups rarely list your bucket.** frost trusts its local record of what's stored and compares it with storage once a week, after a check finds something missing, or when the storage moved. `frost status --verify` always compares.

- **Every backup is a snapshot with a readable ID.** IDs look like `maple-otter-3f1c9a0b2e7`. You can restore by ID or by time, like `latest`, `3 days ago` or `2026-09-20`.

- **Snapshots have no size limit.** A snapshot's file list is stored in chunks like file data, so it fits Permafrost's 16 MiB object limit at any size. The parts that didn't change since the last backup aren't uploaded again.

- **`frost backup --dry-run` shows what would upload.** It lists every file with new data, and the total.

- **Busy files keep their last good copy.** A file that changes while it's read is read again at the end of the backup. If it's still changing, the snapshot keeps its previous copy, and `frost backup` and `frost status` say which files.

- **A missing folder doesn't stop the others.** A configured folder that isn't there, like one on an unplugged drive, is skipped and the rest are backed up. `frost backup` and `frost status` say which one wasn't found.

- **An unreadable folder fails the backup.** Items inside a folder that can't be read are skipped and counted in `frost status`, but a backed-up folder frost can't read at all fails the run rather than saving nothing. On macOS, the error explains how to grant Full Disk Access.

- **Restores say where they go.** Choose `--beside` for a new folder next to the originals, `--to <dir>` for a new folder somewhere else, or `--overwrite` to replace the originals. Only `--overwrite` replaces files, and each file is checked and written to a temporary file before it's moved into place.

- **Restores download in parallel.** Chunks download 8 at a time across all the files being restored. A chunk that repeats, like the empty parts of a disk image, downloads once.

- **Interrupted restores carry on.** If a restore stops, frost prints the command that continues it in the same folder. Files already there are checked and skipped, and the file it was writing picks up from its last good chunk.

- **Restoring over the originals skips what's already right.** `frost restore --overwrite` checks each original against the snapshot first, and files that already match aren't downloaded.

- **`frost browse` opens a snapshot browser.** You can browse snapshots by date, walk the files as they were, compare two snapshots and pick files to restore. It fits small terminals, and a scheduled backup can run while it's open.

- **The browser can pick a restore folder.** "New folder elsewhere" opens Finder, Explorer or a Linux folder picker, and you type the folder when there isn't one.

- **Restores from the browser open in your file manager.** When a restore finishes, frost shows what it restored in Finder, Explorer or your Linux file manager. Nothing opens over SSH or when the restore fails.

- **Every backup checks itself.** Afterwards, frost downloads a random sample of chunks and checks them against their IDs. `frost status` shows the result, and `frost status --verify` runs a check on demand.

- **Storage changes are checked.** `frost config set` checks a new storage location before saving it, and refuses one with no backups, backups made with another key, or a `frost.repo` without its snapshots. `frost init` asks before starting a separate set of backups when this machine's are somewhere else.

- **frost explains when it can't find your backups.** The error and `frost status` say where they were last and how to get back to them, and a scheduled backup that failed because of it shows in `frost status`. Snapshots that disappear from storage are reported too.

- **Backups run on a schedule without a daemon.** `frost init` installs a launchd, systemd, cron or Task Scheduler job depending on your OS. Changing the schedule with `frost config set` updates the job.

- **frost updates itself.** `frost update` installs the latest release, and scheduled backups do it on their own at most once a day. Each release is checked against a signing key built into frost before anything is replaced, and setting `update.auto` to `false` only tells you about new ones.

- **`frost key` manages your recovery phrase.** It shows the phrase behind a confirmation, checks a phrase against your backups, or imports one on a new machine.

- **frost installs with one line.** `install/install.sh` detects macOS, Linux, WSL or Git Bash, checks the download against the release's signed checksums, and installs the binary. The signature check needs `ssh-keygen` from OpenSSH 8.1 or newer.
