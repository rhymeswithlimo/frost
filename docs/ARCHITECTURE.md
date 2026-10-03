# Architecture

frost is a single Go binary with no daemon and no server component. Automatic backups are OS scheduler jobs that run `frost backup`.

## Packages

```
cmd/frost              main, calls cli.Execute
internal/
  cli                  the commands, prompts and output formatting
  config               config.toml, file locations, get/set by key
  crypto               master key, subkeys, sealing objects, chunk IDs
    bip39              recovery phrase encoding (official English wordlist)
  chunker              FastCDC content-defined chunking
  snapshot             snapshot and tree types, "3 days ago" parsing, diffs, restore paths
  repo                 how objects are laid out and named in storage
  manifest             local cache of what's uploaded (bbolt), also the process lock
  engine               backup, restore, verify
  storage              the Backend interface
    s3                 S3-compatible backend (minio-go)
    permafrost         Permafrost HTTP backend and the browser key handoff
    storagetest        in-memory backend and conformance suite, tests only
  schedule             launchd, systemd, cron and Task Scheduler jobs
  update               finding, checking and installing new releases
  desktop              opening the browser and file manager, the folder picker
  sound                sound effects for a hidden game in the browser
  theme                colours, borders and spacing for the CLI and TUI
  tui                  snapshot browser and setup screens (bubbletea, lipgloss)
    assets             wordmarks and sound effects
    demo               the browser on fake data, for development only
```

`cli` uses everything. `engine` builds on `repo`, `manifest` and `chunker`, and `repo` builds on `crypto`, `chunker` and `storage`. `storage` knows nothing about encryption.

## Keys

`frost init` generates one random 256-bit master key. Setup shows it as a 24-word BIP39 phrase, and the key file stores it. Every key frost uses comes from the master key through HKDF-SHA256, each under its own label:

| Subkey | Used for |
|---|---|
| encryption | XChaCha20-Poly1305 for every object |
| chunk id | HMAC-SHA256 that names chunks |
| chunker | The seed for the chunker's gear table |
| fingerprint | A short, non-secret ID shown in `status` |

## Backup

```
walk dirs ─> skip excluded ─> unchanged since last run? ─yes─> reuse chunk list
                                        │ no
                                        v
                      FastCDC chunks ─> HMAC id ─> already uploaded? ─yes─> skip
                                                         │ no
                                                         v
                                   zstd ─> XChaCha20-Poly1305 ─> Put (4 in parallel)
                                                         │
                          all done ─> anything changed? ─no─> no snapshot (verify once a day)
                                                         │ yes
                                                         v
                                   save file list, then header ─> update manifest ─> verify sample
```

1. frost walks each configured folder and skips excluded names and paths. A folder that doesn't exist is skipped and recorded in the snapshot header as missing. Symlinks are recorded as links. Sockets, devices and pipes are ignored, and so are the `.frost-partial-<hash>` files a stopped restore leaves.
2. Unchanged files aren't opened. The manifest remembers each file's size, mtime and chunk list from the last run, and if the size and mtime match and every chunk is known, frost reuses the chunk list.
3. Changed files go through FastCDC. Chunks average 1 MiB, with a 256 KiB minimum and an 8 MiB maximum. The maximum keeps every object within Permafrost's 16 MiB limit.
4. Each chunk's ID is its HMAC. If the manifest already has that ID, nothing is uploaded, so identical data is stored once across files and across runs.
5. New chunks are compressed with zstd when that makes them smaller, encrypted, and uploaded by four workers. Uploaded IDs go into the manifest in batches of 64, so an interrupted run doesn't upload them again.
6. Once every upload succeeds, frost compares the file list with the last snapshot this machine saved (see [Unchanged backups](#unchanged-backups)). If nothing changed, it saves no snapshot and stops here.
7. Otherwise the file list is encoded as JSON and split into chunks like file data, and the pieces that aren't stored yet are uploaded. Then `trees/<id>` (the list of those pieces) is saved, and then the header (`snapshots/<id>`). A snapshot never appears before its data exists, and any upload failure ends the run without saving one.
8. A random sample of chunks is downloaded and checked, and the newest file list is loaded (see [Verification](#verification)). After a run that saved no snapshot, this only happens when the last check is more than a day old or found a problem.

A file whose size or mtime changes while it's read is read again after the walk. If it's still changing, the snapshot keeps the last clean copy from the manifest, or leaves the file out with a warning when there isn't one, because a torn copy of a database or disk image can be worse than none. Chunks read before the change are uploaded anyway and reused once the file settles. Size and mtime checks can't see a change that keeps both the same.

A dry run does steps 1 to 4 and the comparison in step 6, and reports without uploading or saving a snapshot. It still refreshes the local chunk list when a sync is due.

### Unchanged backups

After each snapshot it saves, frost keeps a record in the manifest: the snapshot's ID and paths, a SHA-256 digest of its file list, and 16 bytes per entry (a hash of the path and a hash of the entry). The next backup computes the same from its own file list.

It saves no snapshot only when all of these hold. Anything else, including a lost manifest, saves one.

- The record covers the same paths.
- The digests match.
- The recorded snapshot is the newest one the manifest knows of, so `latest` still means the newest backup.
- Its header is still in storage.

A folder's modification time is left out of the digest, because temporary and excluded files change it without changing anything that's backed up. Adding or removing a folder, or changing its permissions, still counts. A file's modification time counts even when its contents are the same.

The per-entry hashes only count what was added, changed and removed, for `backup` to show. They never decide whether anything changed. The list of known snapshots is refreshed by verification, `status` and the browser, so on storage shared between machines it can be a day out of date, or longer with `verify.sample` set to `0`. A newer snapshot from another machine is then missed until the next refresh.

## Why content-defined chunking

Fixed-size chunks break on inserts. Add one byte at the start of a file and every chunk after it shifts, so the whole file uploads again. FastCDC picks cut points from the content with a rolling gear hash, so they move with the data and an edit only changes the chunk or two around it. The chunker tests check exactly this.

Normalised chunking keeps chunk sizes close to 1 MiB. Before the average size a harder cut condition makes a cut less likely, and after it an easier one makes a cut more likely.

The gear table is seeded from your key. With a public table, the chunk sizes of a known file would be predictable, and a provider could spot that file by its pattern of object sizes.

## Repository layout

A backend stores four kinds of object:

| Key | Content |
|---|---|
| `frost.repo` | Format version, repository ID and creation time. Decrypting it is how frost checks a key |
| `chunks/<ab>/<abcdef...>` | File data and file lists, named by HMAC chunk ID |
| `snapshots/<id>` | The snapshot header: time, host, paths, missing paths, stats, and up to 100 warnings and kept files |
| `trees/<id>` | The chunks that hold the snapshot's file list (path, type, mode, mtime, size, chunk IDs), in order, and its total length |

Every object is sealed as `version(1) | nonce(24) | ciphertext`, with the object's own key as the AEAD associated data. A provider can't rename, swap or replay an object under another name without decryption failing.

Headers and trees are separate so `status` and the browser can list snapshots by fetching small headers, and only load a tree when you open that snapshot.

File lists are stored as chunks, so they have no size limit, every piece fits any backend, and the parts that didn't change since the last snapshot aren't uploaded again. `trees/<id>` is sealed and lists every piece in order with the total length, so no piece can be dropped, swapped or reordered.

`frost.repo`, trees and headers are written with conditional creates (`If-None-Match: *`). Two machines setting up the same storage at once, or two snapshots that draw the same ID, can't overwrite each other. Setup refuses storage that doesn't support this. A snapshot ID is two BIP39 words and 42 more bits in hex, 64 random bits in all.

Decompressed objects are limited to 256 MiB. S3 downloads allow that size plus encryption overhead, and 8 MiB plus overhead for chunks. Permafrost responses keep the protocol's 16 MiB limit. File lists are chunks, and headers cap their lists at 100 entries while keeping full counts, so nothing frost writes gets near these limits.

## Manifest

Each repository has a manifest, `manifest-<repo id>.db` in the cache directory. It's a bbolt file with four buckets: uploaded chunk IDs, the per-file cache, snapshot headers, and state such as the last backup, the last verification and the record of the last saved snapshot (see [Unchanged backups](#unchanged-backups)).

Backups trust the manifest's chunk list and don't list the repository, except when:

- the list is empty, on a new machine or after losing the cache
- the list is more than 7 days old
- the list was checked against a different location
- a verification found a chunk missing

The location is the provider's endpoint, bucket and folder, or the Permafrost server and account. A copy of a repository, or a `frost.repo` moved on its own, therefore gets a full listing before anything in it is trusted. The listing replaces the local list, and missing chunks are uploaded again when their source data is still there. `status --verify` always refreshes it.

This is safe because frost never deletes chunks. A chunk can only disappear from outside, and uploading it again under the same ID repairs every snapshot that uses it. A listing shows that objects exist, not that they're intact. Verification and restore authenticate everything they download.

If the manifest is lost, the chunk list is rebuilt from storage and the file cache refills, which costs one full read of your files.

File-list chunks are deduplicated like file data. If one is deleted outside frost, the newest snapshot, and any older one that shares the piece, can't be opened at all. The check after a backup loads the newest file list in full to catch this. It's the one per-check cost that grows with the size of what's backed up.

`storage-<config>.json` in the cache directory, one per config folder, records where the repository last opened: the storage settings without credentials, and the repository ID. When the repository doesn't open, the error and `status` name that place and the setting that changed. `config set` opens a new location before saving it.

bbolt takes an exclusive file lock, so two frost processes can't use the same manifest at once. It doesn't lock other machines or separate cache directories, which is why repository metadata uses conditional writes.

## Restore

The tree is loaded, and the selected paths and types are checked before anything is written. Chunks download 8 at a time in one ordered stream across every selected file, and a chunk repeated nearby (like the zeros in a disk image) downloads once. Each chunk is authenticated before it's written.

Each file is written to a private `.frost-partial-<hash>` beside its destination, checked against its recorded size, flushed, and renamed into place. The old file isn't deleted before the rename. Modes and mtimes are restored, and failures are reported. Symlinks are created after regular files, so a link can't redirect a later write. Directories get their modes and mtimes last, deepest first.

A new restore folder holds a `.frost-restore` marker, with the snapshot and selection, until the restore finishes. Running the same restore again finds the folder by its marker and carries on. Files already in place are skipped, and a partial file keeps the leading chunks that still match. Both are checked by chunking what's on disk and comparing keyed chunk IDs, so nothing is trusted by its name. This works because content-defined cut points depend only on the bytes since the last cut, so the start of a file splits into the same leading chunks as the whole. `--overwrite` does the same checks, so originals that already match aren't downloaded. The marker and each partial file are locked while a restore writes them, so two restores can't share them. A partial file whose data doesn't match the snapshot's recorded size is deleted.

Restores into a folder go through confined directory handles (`os.Root`). They reject symlinked parents, path traversal, duplicate destinations, and Windows device or alternate-stream paths. A new folder is created exclusively, unless it holds an unfinished restore of the same selection. Paths inside it are relative to the deepest folder the selection's parents share (`RestoreOptions.Base`), and anything outside that is refused before writing.

An in-place restore needs native absolute paths. It resolves the links on the path to the deepest existing parent itself, following only links owned by root or the current user (on Windows, none), then opens the resolved folder as the root handle, so nothing below it can redirect the write. Only overwrite restores replace files. A restore is atomic per file, not across the whole selection.

Restore doesn't need the manifest. The browser restores with only the repository, after releasing the manifest's lock.

## Verification

`engine.Verify` refreshes the snapshot headers and samples N chunk IDs from the manifest's chunk list. It downloads them 8 at a time, authenticates each one and checks its HMAC. It also loads the newest snapshot's file list and checks that every chunk it uses is in the chunk list.

A backup runs it after saving a snapshot. A backup that saved nothing new runs it only when there's no earlier result, the last one found a problem, or it's more than a day old, and otherwise shows the last result.

Verification only lists the repository when a sync is due, and always for `status --verify`. A chunk found missing makes the next backup sync and upload it again. Results are saved for `status` and the browser. A snapshot this machine knew about that has gone from storage counts as a failure once, and is then forgotten.

Sampling can find corruption, but it doesn't prove that every snapshot can be restored.

## Scheduling

`init` and `config set schedule.*` write a native job and nothing else:

| OS | Job |
|---|---|
| macOS | A launchd agent with `StartInterval` and low-priority I/O |
| Linux | A systemd user timer with `OnCalendar`, `Persistent=true` and up to 5 minutes of random delay, or a crontab line when systemd isn't running |
| Windows | A Task Scheduler task, created with `schtasks` |

The job files and arguments are built by pure functions (`LaunchdPlist`, `SystemdUnits`, `CronLine`, `TaskArgs`) and tested as strings. launchd and cron redirect output. On Windows, `cli` opens the log itself and records backup errors before its deferred update check. `TaskArgs` passes the log path through `--log-file`; older tasks use the default cache path. [CLI.md](CLI.md#files) lists log locations and trimming.

## Updates

`internal/update` finds the newest release by following GitHub's `/releases/latest` redirect, which has no API rate limit. It checks the signed `checksums.txt` against the pinned release key in `key.go`, then downloads and checks the archive for this platform. The same key is in `install/release-signing.pub` and `install/install.sh`, and a test keeps all three the same. The new binary is staged next to the old one, run once with `--version`, and renamed into place. A lock file next to the binary stops two updates running at once.

`cli` calls it from `frost update`, and after `frost backup --scheduled` at most every 20 hours. The result goes in `update.json` in the cache directory, which `status` and the browser read without going online.

## Storage backends

```go
type Backend interface {
    Put(ctx context.Context, key string, data []byte) error
    PutNew(ctx context.Context, key string, data []byte) error // storage.ErrExists if present
    Get(ctx context.Context, key string) ([]byte, error)      // storage.ErrNotFound if missing
    List(ctx context.Context, prefix string) ([]string, error)
    Delete(ctx context.Context, key string) error
    String() string
}

// Optional. Without it, String is the location.
type Locator interface {
    Location() string
}
```

To add a backend, implement `Backend` and run `storagetest.Conformance` against it in a test. If `String` can describe two different places the same way (one bucket name at two providers, say), implement `Locator` too, so the manifest notices when storage moves. The S3 tests use an in-process fake S3 server. The Permafrost tests use a reference server written from [PERMAFROST.md](PERMAFROST.md).

## TUI

`internal/tui` holds the snapshot browser and the `frost init` setup screens. The browser has home, snapshots, files, diff and restore screens, with help and settings overlays. Network work (listing snapshots, loading trees, diffing, restoring) runs in bubbletea commands, so the UI never blocks. A restore streams its progress over a channel.

The browser indexes subtree file counts and sizes once per tree, caches snapshot date headings, and reuses downloaded headers on refresh. It reads what it needs from the manifest up front and releases the lock, so a scheduled backup can run while it's open.

Setup and browser work is cancelled when the program closes. Checkout cleanup runs even if the program exits before receiving its start message. Folder picker and game messages carry the attempt they belong to, so one from an earlier attempt is ignored.

Colours, borders and spacing all come from `internal/theme`.
