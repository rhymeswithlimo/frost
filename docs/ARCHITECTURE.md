# Architecture

frost is a singular Go binary. No daemon, no server component.

Automatic backups are jobs scheduled on the OS that run `frost backup`.

## Packages

```
cmd/frost              main, calls cli.Execute
internal/
  cli                  the seven commands, prompts, output formatting
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
                          all done ─> save tree, then header ─> update manifest ─> verify sample
```

1. **Walk.** Each configured directory is walked. One that doesn't exist is skipped and listed in the header as missing. Excluded names and paths are skipped. Symlinks are recorded. Sockets and devices are ignored.
2. **Skip unchanged files.** The manifest remembers each file's size, mtime and chunk list from the last run. If size and mtime match and every chunk is known, the file isn't opened.
3. **Chunk.** Changed files go through FastCDC. Chunks average 1 MiB (256 KiB min, 8 MiB max).
4. **Deduplicate.** Each chunk's ID is its HMAC. If the manifest already has that ID, nothing's uploaded. This works across files and across runs.
5. **Upload.** New chunks are compressed with zstd (only when that makes them smaller), encrypted, and uploaded by four workers. Uploaded IDs go into the manifest in batches of 64, so an interrupted run doesn't re-upload everything next time.
6. **Commit.** Once every upload succeeds, the file list (`trees/<id>`) is saved, then the header (`snapshots/<id>`). A snapshot therefore never appears before its data exists. Any upload failure aborts the run without saving a snapshot.
7. **Verify.** A random sample of chunks is downloaded and checked.

A dry run does steps 1 to 4 and reports without uploading objects or saving a snapshot. It refreshes the local chunk-presence cache. Unchanged files can still reuse their cached chunk lists.

## Why content-defined chunking

Fixed-size chunks break on inserts: add one byte at the start of a file and every chunk after it shifts, so all of it re-uploads. FastCDC picks boundaries from the content with a rolling gear hash, so boundaries move with the data. An edit changes the chunk or two around it and nothing else. The chunker tests check exactly this.

The gear table is seeded from your key. With a public table, the sequence of chunk sizes for a known file would be predictable, and a provider could spot that file by its pattern of object sizes.

Normalized chunking (a harder cut condition before the average size, an easier one after) keeps chunk sizes close to 1 MiB.

## Repository layout

Everything a backend stores:

| Key | Content |
|---|---|
| `frost.repo` | Format version, repository ID, creation time. Decrypting it is how frost checks a key |
| `chunks/<ab>/<abcdef...>` | File data, named by HMAC chunk ID |
| `snapshots/<id>` | Snapshot header: time, host, paths, missing paths, stats |
| `trees/<id>` | Snapshot file list: path, type, mode, mtime, size, chunk IDs |

Every object is sealed as `version(1) | nonce(24) | ciphertext`, and the object's own key is the AEAD associated data. A provider can't rename, swap or replay an object under another name without the decryption failing.

Headers and trees are split so `status` and the browser can list snapshots by fetching small headers, and only load a tree when you open that snapshot.

New snapshot IDs contain 64 random bits; older short IDs remain valid. Decompressed objects are limited to 256 MiB. S3 downloads allow that size plus encryption overhead, with an 8 MiB plus overhead limit for chunks. Permafrost responses retain the protocol's 16 MiB limit. Large trees exceeding these limits require a future paged format.

## Manifest

`manifest-<repo id>.db` in the cache directory, one per repository. It's a bbolt file with four buckets: uploaded chunk IDs, per-file cache, snapshot headers, and small bits of state (last backup, last verification).

Each backup refreshes the chunk set from storage before reusing cached file entries. Missing chunks are uploaded again when their source data is still available. If the manifest is missing, the file cache refills, costing one full read of your files. Object listings prove presence, not integrity; verification and restore authenticate downloaded data.

bbolt takes an exclusive file lock, which prevents concurrent operations using the same local manifest. It doesn't lock other machines or separate cache directories. Repository metadata uses atomic conditional writes to prevent overwrites during concurrent initialization or snapshot-ID collisions. S3-compatible servers must support `If-None-Match: *` on object PUTs.

## Restore

The tree is loaded and the selected paths and types are validated before writing. Each file's chunks are authenticated, written to a private temporary file, checked against the recorded total size and flushed before replacement. The old file isn't deleted before rename. Modes and mtimes are restored, and failures are reported. Symlinks are staged and renamed after regular files. Directories get their mtimes last, deepest first.

Targeted restores use confined directory handles and reject symlink parents, traversal, duplicate destinations and Windows device or alternate-stream paths. In-place restore requires native absolute paths and refuses symlinked parent paths. Default CLI and browser targets must be new directories; explicit `--target` can replace files in an existing directory. A restore is transactional per file, not across the whole selection.

## Verification

`engine.Verify` refreshes remote snapshot headers and adds remote chunk IDs to the local sampling set. It samples N chunk IDs, authenticates each and checks its HMAC. It also opens the newest snapshot's tree and checks its chunk references against the remote listing. Results are saved for `status` and the browser. Sampling can detect corruption but doesn't prove that every snapshot is recoverable. `status --verify` returns an error when verification fails.

Files that visibly change during reading are skipped with a warning. Size and mtime caching can't detect changes that preserve both values. frost doesn't take filesystem or database snapshots, preserve ownership, ACLs, extended attributes or hard-link relationships, or guarantee a consistent backup of actively written databases.

## Scheduling

`init` and `config set schedule.*` write a native job and nothing else:

- **macOS:** a launchd agent with `StartInterval`
- **Linux:** a systemd user timer with `OnCalendar` and `Persistent=true`, or a crontab line if systemd isn't there
- **Windows:** a Task Scheduler task via `schtasks`

The generated files are built by pure functions (`LaunchdPlist`, `SystemdUnits`, `CronLine`, `TaskArgs`) and tested as strings.

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

Styling (e.g. colors, border and spacing) is derived from `internal/theme`.
