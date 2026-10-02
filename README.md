<h3 align="center">frost</h3>

<p align="center">Encrypted, incremental backups to storage you choose.</p>

<p align="center">
  <a href="https://getfro.st">Website</a> ·
  <a href="#running-locally">Development</a> ·
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

frost backs up your folders to S3-compatible storage or [Permafrost](https://example.com), and encrypts everything on your machine before it's uploaded. It's one Go binary with no daemon. Backups run from your OS scheduler, and `frost browse` opens your snapshots in the terminal.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/rhymeswithlimo/frost/main/install/install.sh | sh
```

#### Manually

Download the archive for your platform from the [latest release](https://github.com/rhymeswithlimo/frost/releases/latest), extract it, and put `frost` somewhere on your `PATH`. To check the download first, see [Verifying a download by hand](docs/SECURITY.md#verifying-a-download-by-hand).

## Get started

Learn how to use frost at [getfro.st/docs](https://getfro.st/docs).

## Contributing

Contributions are welcome. Small fixes can go straight to a pull request, and anything bigger starts with an issue so we can agree on the approach first. **Read [CONTRIBUTING.md](docs/CONTRIBUTING.md) before you start.** It covers the process and the rules every change has to follow.

**Report security problems privately, never in a public issue.** [SECURITY.md](docs/SECURITY.md#reporting-a-vulnerability) explains how.

## Learn more

| Doc | Covers |
|---|---|
| [Architecture](docs/ARCHITECTURE.md) | The packages, how backup and restore work, and the repository format |
| [Security](docs/SECURITY.md) | The encryption model and what a storage provider can see |
| [Permafrost API](docs/PERMAFROST.md) | The storage protocol, for anyone running a compatible server |
| [Changelog](docs/CHANGELOG.md) | What changed in each release |

## Running locally

You need Go (the version in `go.mod`). There's no cgo, and the tests don't need any external services.

```sh
go build ./cmd/frost    # build the binary
go test ./...           # run the tests
go vet ./...            # static checks
```

To run the CLI from source without touching your real setup, point it at throwaway folders:

```sh
export FROST_CONFIG_DIR=/tmp/frost-dev/config FROST_CACHE_DIR=/tmp/frost-dev/cache
go run ./cmd/frost <command>
```

`frost init` installs a real scheduled job when automatic backups are on, so turn them off while developing. A build from source reports its version as `dev` and can't update itself.

To work on the TUI without storage or a key, run `go run ./internal/tui/demo`. It opens the snapshot browser on fake data, and these flags change what it shows:

| Flag | Shows |
|---|---|
| `-setup` | The `frost init` screens instead of the browser |
| `-latency 400ms` | Slow storage, so you can see the loading states |
| `-empty` | A repository with no snapshots |
| `-broken` | A failed backup and a failed health check |

`FROST_TUI_DUMP=<dir> go test ./internal/tui` writes every screen's ANSI output to `<dir>`.

## License

BSD 3-Clause, see [LICENSE](LICENSE).
