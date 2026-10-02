<div align="center">
   <img alt="frost" src="public/repo/README_Hero_Banner_PNG_v2.2__frost.png" width="280px">
</div>

<p align="center">Backup your files. Encrypted, incrementally to storage you choose.</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quickstart">Quickstart</a> ·
  <a href="docs/CLI.md">Docs</a> ·
  <a href="docs/CHANGELOG.md">Changelog</a> ·
  <a href="#license">License</a>
</p>

<p align="center">
<a href="https://github.com/rhymeswithlimo/frost/releases"><img src="https://img.shields.io/github/v/release/rhymeswithlimo/frost?color=1926c4&style=flat-square" alt="Latest release"></a>
<a href="https://github.com/rhymeswithlimo/frost/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/rhymeswithlimo/frost/ci.yml?branch=main&label=CI&style=flat-square" alt="CI"></a>
<a href="LICENSE"><img src="https://img.shields.io/badge/license-BSD--3--Clause-1926c4?style=flat-square" alt="License: BSD 3-Clause"></a>
<img src="https://img.shields.io/badge/Go-1.26.6%2B-1926c4?logo=go&logoColor=white&style=flat-square" alt="Go 1.26.6+">
<img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-1926c4?style=flat-square" alt="macOS, Linux, Windows">
</p>

---

frost is a single binary that backs up your directories to an S3-compatible bucket or to [Permafrost](https://example.com), on a schedule. Backup data is encrypted on your machine before it's uploaded.

frost generates your encryption key locally and shows it as a 24 word recovery phrase. That phrase is the only thing protecting your backups: whoever has it can read them, and if you lose it (and the machine it's stored on) they can't be recovered by anyone. Storage providers may require an account and separate credentials.

- **Client-side encryption**: XChaCha20-Poly1305 with a key that never leaves your machine.
- **Only uploads what's changed**: content-defined chunking means an edit in the middle of a big file re-uploads a chunk or two, not the whole file.
- **Readable snapshots**: every run gets an ID like `maple-otter-3f1c`. Restore by ID or by time (`3 days ago`, `yesterday`, `2026-09-20`).
- **Terminal browser**: browse snapshots, diff them and pick files to restore in frost's very own TUI.
- **Checks itself**: after each backup, frost re-downloads a random sample of chunks and verifies them, so problems show up in `frost status` before you need a restore.
- **Runs on a schedule**: launchd, systemd, cron or Task Scheduler, set up for you during `frost init`.

<div align="center">
   <img alt="frost showcase" src="public/repo/README_Hero_PNG_v2.1__frost.png" width="100%">
</div>

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/rhymeswithlimo/frost/main/install/install.sh | sh
```

Works on macOS, Linux and Windows (through Git Bash). The installer verifies the signed checksums, checks the archive's SHA-256 and installs the binary. If its directory isn't on your PATH, it prints instructions to add it.

If you prefer doing it by hand, grab an archive from [Releases](https://github.com/rhymeswithlimo/frost/releases), or:

```sh
go install github.com/rhymeswithlimo/frost/cmd/frost@latest
```

Builds from source don't update themselves. Run `go install` again for a new version.

## Quickstart

```sh
frost init                  # pick directories, schedule and storage; save your recovery phrase
frost backup --dry-run      # see exactly what would be uploaded
frost backup                # back up now
frost status                # recent snapshots, next run, health
frost restore "2 days ago" ~/Documents/report.pdf --beside
frost browse                # browse snapshots, diff them, pick files to restore
```

Every restore says where it goes: `--beside` (a new folder next to the originals), `--to <dir>` (a new folder inside `<dir>`) or `--overwrite` (back over the originals, asks first). Only `--overwrite` can replace existing files.

> [!IMPORTANT]
> Save your recovery phrase somewhere safe, like on paper or in a password manager. It's the only way to decrypt your backups, there's no way to reset it, and nobody (including us and your storage provider) can recover it for you. `frost init` shows it once. Run `frost key verify` now and then to check what you wrote down is right, and `frost key import` to put it on a new machine.

> [!NOTE]
> On macOS, folders like `~/Documents` and `~/Desktop` are privacy protected. If a backup fails with "operation not permitted", add the `frost` binary under System Settings > Privacy & Security > Full Disk Access.

## Commands

```text
frost <command> [flags]

COMMANDS:
   init                          interactive setup: directories, schedule, storage, key
   backup                        back up the configured directories now
   restore [snapshot] [paths]    restore a whole snapshot or chosen paths
   status                        recent snapshots, last and next run, verification health
   browse                        open the snapshot browser (TUI)
   config [get|set|edit]         print or change settings
   key <show|verify|import>      manage your recovery phrase
   update                        install the latest release

GLOBAL (every command):
   --config-dir string           use a different config directory
   -h, --help                    show help for a command

BACKUP:
   -n, --dry-run                 list what would upload; don't upload or save a snapshot
   --path string                 back up this directory instead (repeatable)
   --exclude string              also skip this pattern for this run (repeatable)
   --no-verify                   skip the post-backup spot check

RESTORE:
   --beside                      restore into a new folder next to the originals
   --to string                   restore into a new folder inside this directory
   --overwrite                   restore over the originals (asks first)
   -y, --yes                     don't ask before overwriting

STATUS:
   --verify                      run a fresh verification first
   -a, --all                     list every snapshot

CONFIG:
   --show-secrets                show credentials instead of masking them

UPDATE:
   --check                       only say whether there's a newer release
```

A snapshot can be `latest`, an ID or its prefix (`maple`), a relative time (`12h`, `2w`, `3 days ago`), `yesterday`, or a date (`2026-09-20 14:30`). You always get the newest snapshot at or before that point. With no arguments in a terminal, `restore` opens the browser.

Full reference, including every setting and environment variable: [docs/CLI.md](docs/CLI.md).

## Snapshots and storage

Each backup creates a snapshot: a record of what your files looked like at that moment. Restoring a snapshot gives you your files back as they were then, so an old snapshot is how you recover a file you changed or deleted last week.

frost splits your files into small chunks and uploads each chunk once, encrypted. A snapshot is a list of files and the chunks that make them up. When a file hasn't changed, or two files share content, the existing chunks are reused. That's why the second backup is quick and why keeping lots of snapshots doesn't multiply the size of your storage.

In your bucket you'll see a `frost/` folder (change it with `storage.s3.prefix`) holding a few folders (`chunks/`, `snapshots/`, `trees/`) and a `frost.repo` file. They're all encrypted, and without the recovery phrase they can't be read, including file names. The storage provider can see how many objects there are, their sizes and when you upload, but not what's in them. Details are in [docs/SECURITY.md](docs/SECURITY.md).

Your recovery phrase is stored in plain text in frost's config folder, so scheduled backups can run without you. Use disk encryption and a locked screen on that machine.

### Backends

| Backend | For | Setup |
|---|---|---|
| `permafrost` | Storage you connect to with a single access key | Access key. API: [docs/PERMAFROST.md](docs/PERMAFROST.md) |
| `s3` | Any S3-compatible bucket: AWS S3, Backblaze B2, Cloudflare R2, Wasabi, MinIO, Garage | Bucket and access key. `frost init` has presets for the big providers. |

You pick one during `frost init`. Credentials can also come from the environment (`FROST_S3_ACCESS_KEY_ID`, `FROST_PERMAFROST_TOKEN` and friends), and those are never written to `config.toml`.

## Documentation

- [CLI reference](docs/CLI.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Security model](docs/SECURITY.md)
- [Permafrost API](docs/PERMAFROST.md)
- [Changelog](docs/CHANGELOG.md)

## Development

frost is one Go module with no cgo, so a plain Go toolchain is all you need.

```sh
go build ./cmd/frost          # build the binary
go test ./...                 # run the test suite
go vet ./...                  # static checks
go run ./internal/tui/demo    # try the TUI against fake data
```

The demo takes `-latency 400ms`, `-empty` and `-broken` to simulate slow, empty and failing repositories. See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) before opening a pull request, and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how the pieces fit together.

## License

BSD 3-Clause, see [LICENSE](LICENSE).
