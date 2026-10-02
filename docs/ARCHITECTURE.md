# Architecture

frost is a singular Go binary. No daemon, no server component.

Automatic backups are jobs scheduled on the OS that run `frost backup`.

## Packages

```
cmd/frost              main, calls cli.Execute
internal/
  cli                  the commands, prompts, output formatting
  config               config.toml, file locations, get/set by key
  crypto               master key, subkeys, sealing blobs, chunk IDs
    bip39              recovery phrase encoding (official English wordlist)
  chunker              FastCDC content-defined chunking
  snapshot             snapshot and tree types, "3 days ago" parsing, diff
  repo                 how objects are laid out and named in storage
  manifest             local cache of what's uploaded (bbolt)
  engine               backup, restore, verify
  storage              the Backend interface
    s3                 S3-compatible backend (minio-go)
    permafrost         Permafrost HTTP backend
    storagetest        in-memory backend and conformance suite, tests only
  schedule             launchd, systemd, cron and Task Scheduler jobs
  update               finding, checking and installing new releases
  theme                colours, borders and spacing for the CLI and TUI
  tui                  snapshot browser and setup screens (bubbletea, lipgloss)
    assets             the wordmark
```

Dependencies point down the list: `cli` uses everything, `engine` uses `repo`, `manifest` and `chunker`, `repo` uses `crypto` and `storage`, and `storage` knows nothing about encryption.

## Keys

Master keys are one random 256-bit code created by `frost init`. It's shown once as a BIP39 phrase and stored in the key file. Every key frost actually uses comes from the master key through HKDF-SHA256 with a distinct label:

| Subkey | Used for |
|---|---|
| encryption | XChaCha20-Poly1305 for every object |
| chunk id | HMAC-SHA256 that names chunks |
| chunker | Seeds the chunker's gear table |
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
                          all done ─> save file list, then header ─> update manifest ─> verify sample
```

1. **Walk.** Each configured directory is walked. One that doesn't exist is skipped and listed in the header as missing. Excluded names and paths are skipped. Symlinks are recorded. Sockets and devices are ignored, and so are the `.frost-partial-<hash>` files a stopped restore leaves.
2. **Skip unchanged files.** The manifest remembers each file's size, mtime and chunk list from the last run. If size and mtime match and every chunk is known, the file isn't opened.
3. **Chunk.** Changed files go through FastCDC. Chunks average 1 MiB (256 KiB min, 8 MiB max). The max stays at 8 MiB so every object fits Permafrost's 16 MiB limit.
   A file that changes while it's read (its size or mtime moves) is read again after the walk. If it's still changing, the snapshot keeps its last clean copy from the manifest, or leaves the file out with a warning if there isn't one. A torn copy of a database or disk image can be worse than none. Data read before the change is uploaded anyway and reused once the file settles.
4. **Deduplicate.** Each chunk's ID is its HMAC. If the manifest already has that ID, nothing's uploaded. This works across files and across runs.
5. **Upload.** New chunks are compressed with zstd (only when that makes them smaller), encrypted, and uploaded by four workers. Uploaded IDs go into the manifest in batches of 64, so an interrupted run doesn't re-upload everything next time.
6. **Commit.** Once every upload succeeds, the file list is encoded as JSON and split into chunks like file data, and the pieces that aren't stored yet are uploaded. Then `trees/<id>` (the list of those pieces) is saved, then the header (`snapshots/<id>`). A snapshot therefore never appears before its data exists. Any upload failure aborts the run without saving a snapshot.
7. **Verify.** A random sample of chunks is downloaded and checked, and the newest file list is loaded.

A dry run does steps 1 to 4 and reports without uploading objects or saving a snapshot. It refreshes the local chunk list when a sync is due. Unchanged files can still reuse their cached chunk lists.

## Why content-defined chunking

Fixed-size chunks break on inserts: add one byte at the start of a file and every chunk after it shifts, so all of it re-uploads. FastCDC picks boundaries from the content with a rolling gear hash, so boundaries move with the data. An edit changes the chunk or two around it and nothing else. The chunker tests check exactly this.

The gear table is seeded from your key. With a public table, the sequence of chunk sizes for a known file would be predictable, and a provider could spot that file by its pattern of object sizes.

Normalized chunking (a harder cut condition before the average size, an easier one after) keeps chunk sizes close to 1 MiB.

## Repository layout

Everything a backend stores:

| Key | Content |
|---|---|
| `frost.repo` | Format version, repository ID, creation time. Decrypting it is how frost checks a key |
| `chunks/<ab>/<abcdef...>` | File data and file lists, named by HMAC chunk ID |
| `snapshots/<id>` | Snapshot header: time, host, paths, missing paths, stats, the first 100 warnings and kept files |
| `trees/<id>` | The chunks holding the snapshot's file list (path, type, mode, mtime, size, chunk IDs), in order, and its length |

Every object is sealed as `version(1) | nonce(24) | ciphertext`, and the object's own key is the AEAD associated data. A provider can't rename, swap or replay an object under another name without the decryption failing.

Headers and trees are split so `status` and the browser can list snapshots by fetching small headers, and only load a tree when you open that snapshot.

File lists are stored as chunks so they have no size limit and fit any backend, and so the parts that didn't change since the last snapshot aren't uploaded again. `trees/<id>` is sealed and lists every piece in order, so none can be dropped or swapped.

New snapshot IDs contain 64 random bits; older short IDs remain valid. Decompressed objects are limited to 256 MiB. S3 downloads allow that size plus encryption overhead, with an 8 MiB plus overhead limit for chunks. Permafrost responses retain the protocol's 16 MiB limit. File lists are chunks, and headers cap their lists at 100 entries with full counts, so nothing frost writes gets near these limits.

## Manifest

`manifest-<repo id>.db` in the cache directory, one per repository. It's a bbolt file with four buckets: uploaded chunk IDs, per-file cache, snapshot headers, and small bits of state (last backup, last verification).

Backups trust the manifest's chunk list and don't list the repository, except when the list is empty (a new machine or a lost cache), more than 7 days old, checked against a different location, or a verification found a chunk missing. The location is the provider's endpoint, bucket and folder, or the Permafrost server and account, so a copy of a repository or a `frost.repo` moved on its own gets a full listing before anything is trusted. Then it's replaced with what's in storage, and missing chunks are uploaded again when their source data is still available. `status --verify` always refreshes it. This is safe because frost never deletes chunks: one can only disappear from outside, and uploading it again under the same ID repairs every snapshot that uses it. If the manifest is missing, the file cache refills, costing one full read of your files. Object listings prove presence, not integrity; verification and restore authenticate downloaded data.

File-list chunks are deduplicated against the same list. If one is deleted outside frost, the newest snapshot (and any older one sharing that piece) can't be opened, not just one file. The check after each backup loads the newest file list in full to catch that. It's the one per-backup cost that grows with the size of what's backed up.

`storage-<config>.json` in the cache directory (one per config folder) records where the repository last opened (settings without credentials, and its ID). When it doesn't open, the error and `status` name that place and the setting that changed. `config set` opens a new location before saving it.

bbolt takes an exclusive file lock, which prevents concurrent operations using the same local manifest. It doesn't lock other machines or separate cache directories. Repository metadata uses atomic conditional writes to prevent overwrites during concurrent initialization or snapshot-ID collisions. S3-compatible servers must support `If-None-Match: *` on object PUTs.

## Restore

The tree is loaded and the selected paths and types are validated before writing. Chunks download 8 at a time in one ordered stream across every selected file, and a chunk repeated nearby (like the zeros in a disk image) downloads once. Each chunk is authenticated before it's written. A file is written to a private `.frost-partial-<hash>` beside its destination, checked against the recorded total size and flushed, then renamed into place. The old file isn't deleted before rename. Modes and mtimes are restored, and failures are reported. Symlinks are staged and renamed after regular files. Directories get their mtimes last, deepest first.

A new restore folder holds a `.frost-restore` marker (snapshot and selection) until the restore finishes. Running the same restore again finds the folder by it and carries on. Files already in place are skipped, and a partial file keeps the leading chunks that still match. Both are checked by chunking what's on disk and comparing keyed chunk IDs, so nothing is trusted by its name. That works because content-defined cut points depend only on the bytes since the last cut, so a prefix of a file splits into the same leading chunks. `--overwrite` does the same checks, so originals that already match aren't downloaded. The marker and each partial file are locked while a restore writes them, so two restores can't share them. A partial file whose data turns out not to match the snapshot's recorded size is deleted. Overwriting needs room for the new copy of a file next to the old one until the rename.

Targeted restores use confined directory handles and reject symlink parents, traversal, duplicate destinations and Windows device or alternate-stream paths. In-place restore requires native absolute paths. It resolves the links on the path to the deepest existing parent itself, following only those owned by root or the current user (Windows refuses any link), then opens the resolved folder as the root handle, so nothing below it can redirect the write. Beside and new-location restores create a new directory exclusively, or continue an unfinished one for the same selection, and store paths relative to the deepest folder the selection's parents share (`RestoreOptions.Base`); anything outside it is refused before writing. Only overwrite restores replace files. A restore is transactional per file, not across the whole selection.

## Verification

`engine.Verify` refreshes snapshot headers and samples N chunk IDs from the manifest's chunk list, downloading 8 at a time, authenticating each and checking its HMAC. It also loads the newest snapshot's file list and checks every chunk it uses is in that list. It only lists the repository when a sync is due, and always for `status --verify`. A chunk found missing makes the next backup sync and upload it again. Results are saved for `status` and the browser. Sampling can detect corruption but doesn't prove that every snapshot is recoverable. `status --verify` returns an error when verification fails.

A file that visibly changes while it's read is read again once. If it's still changing, the snapshot keeps its last clean copy, or skips it with a warning when there isn't one. Size and mtime caching can't detect changes that preserve both values. frost doesn't take filesystem or database snapshots, preserve ownership, ACLs, extended attributes or hard-link relationships, or guarantee a consistent backup of actively written databases.

## Scheduling

`init` and `config set schedule.*` write a native job and nothing else:

- **macOS:** a launchd agent with `StartInterval`
- **Linux:** a systemd user timer with `OnCalendar` and `Persistent=true`, or a crontab line if systemd isn't there
- **Windows:** a Task Scheduler task via `schtasks`

The generated files are built by pure functions (`LaunchdPlist`, `SystemdUnits`, `CronLine`, `TaskArgs`) and tested as strings.

## Updates

`internal/update` finds the newest release by following the `/releases/latest` redirect on GitHub, which has no API rate limit. It checks the signed `checksums.txt` with the pinned release key (`key.go`, also in `install/release-signing.pub` and `install/install.sh`, and a test keeps all three the same), then downloads and checks the archive for this platform. The new binary is staged next to the old one, run once with `--version`, and renamed into place. A lock file next to the binary stops two updates running at once.

`cli` calls it from `frost update` and after `frost backup --scheduled`, at most every 20 hours. What happened is saved to `update.json` in the cache directory, which `status` and the browser read without going online.

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
```

To add a backend, implement this and run `storagetest.Conformance` against it in a test. The S3 tests use an in-process fake S3 server. The Permafrost tests use a reference server written from [PERMAFROST.md](PERMAFROST.md).

## TUI

`internal/tui` is a single bubbletea-based screen field: home, snapshots, files, diff, restore, plus help and settings overlays. Network work (listing snapshots, loading trees, diffing, restoring) runs in commands so the UI never blocks. A restore streams progress over a channel.

Tree indexing also runs in a command. The browser indexes subtree file counts and sizes once, caches snapshot date headings, and reuses downloaded headers on refresh. Setup and browser work is cancelled when the program closes. Checkout cleanup runs even if the program exits before receiving its start message; picker and game messages identify their originating attempt.

Styling (e.g. colors, border and spacing) is derived from `internal/theme`.
