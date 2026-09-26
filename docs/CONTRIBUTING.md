# Contributing

Thanks for helping. Security problems go through private reporting, not issues. See [SECURITY.md](SECURITY.md).

## How it works

1. Small fix: open a PR.
2. Anything bigger, or anything touching encryption, the storage format, dependencies or the command list: open an issue first so we can agree on the approach.
3. CI passes, a maintainer reviews, it gets squash merged.

You don't need to touch the changelog. The maintainer writes it when merging.

## Setup

You need Go (the version in `go.mod`) and git.

```sh
git clone https://github.com/rhymeswithlimo/frost
cd frost
go build ./cmd/frost
go test ./...
```

The tests don't need any external services.

To try the CLI without touching your real setup:

```sh
export FROST_CONFIG_DIR=/tmp/frost-dev/config FROST_CACHE_DIR=/tmp/frost-dev/cache
```

`frost init` with automatic backups on installs a real scheduled job. Say no while developing, or remove it with `frost config set schedule.enabled false`.

To try the snapshot browser on fake data, run `go run ./internal/tui/demo`.

[ARCHITECTURE.md](ARCHITECTURE.md) explains how the code is laid out.

## Rules

- New behaviour comes with a test.
- Never log, print or send the key or phrase, except in `frost key show` and `init`.
- Changes to the on-disk or in-bucket format need a version bump in `internal/repo` and a migration.
- "frost" is always lowercase.

CI checks formatting, vet and tests on Linux, macOS and Windows.
