# Contributing

Thanks for helping. Report security problems privately, never as issues ([SECURITY.md](SECURITY.md#reporting-a-vulnerability) explains how).

## What's likely to be accepted

The changes most likely to be accepted are:

- Bug fixes
- Fixes for a specific OS, filesystem or scheduler
- Fixes for S3-compatible providers that don't work yet
- Faster or lighter backups, restores and verification
- Tests for behaviour that isn't covered yet
- Documentation fixes

Some changes need agreement in an issue before you write any code: anything that touches encryption, the storage format or dependencies, a new command or storage backend, and any change to how the TUI looks. frost keeps a small, fixed set of commands, so a new feature usually belongs as a flag or an action on an existing one.

If you're not sure a change fits, ask in an issue, or pick one labelled [`help wanted`](https://github.com/rhymeswithlimo/frost/issues?q=is%3Aissue%20state%3Aopen%20label%3A%22help%20wanted%22), [`good first issue`](https://github.com/rhymeswithlimo/frost/issues?q=is%3Aissue%20state%3Aopen%20label%3A%22good%20first%20issue%22) or [`bug`](https://github.com/rhymeswithlimo/frost/issues?q=is%3Aissue%20state%3Aopen%20label%3Abug). To take on an issue, leave a comment on it, and a maintainer will assign it to you unless someone's already working on it.

> [!NOTE]
> Pull requests that ignore these guidelines will likely be closed.

## How it works

1. For a small fix, open a pull request against `main`.
2. For anything that needs agreement, open an issue first, and wait for the approach to be agreed before you open a pull request.
3. Once CI passes and a maintainer has reviewed it, the pull request is squash merged.

You don't need to touch the changelog. The maintainer writes it when merging.

## Setup

Clone the repository with `git clone https://github.com/rhymeswithlimo/frost`, then follow [Running locally](../README.md#running-locally) in the README. If you've already installed a scheduled job while developing, remove it with `frost config set schedule.enabled false`.

[ARCHITECTURE.md](ARCHITECTURE.md) explains how the code is laid out.

## Rules

- New behaviour comes with a test.
- Never log, print or send the key or phrase, except in `frost key show` and `frost init`.
- A change to the on-disk or in-bucket format needs a version bump in `internal/repo` and a migration.
- A change to the Permafrost API changes [PERMAFROST.md](PERMAFROST.md) and the reference server in `internal/storage/permafrost/server_test.go` together.
- Docs, help text, output and comments follow [STYLE.md](STYLE.md).
- "frost" is always lowercase, even at the start of a sentence.

## Before you open a pull request

Run the same checks CI runs, from the repository root:

```sh
gofmt -l .      # should print nothing
go vet ./...
go test ./...
```

If you changed `install/install.sh` or `scripts/release.sh`, run shellcheck on it too. For a change to the TUI, try it in the demo (`go run ./internal/tui/demo`) at a few window sizes, small ones included.

CI runs the build, vet and tests on Linux, macOS and Windows, with the race detector on Linux and macOS. It also checks `gofmt`, runs shellcheck on the scripts, and cross-compiles every release target. govulncheck checks dependencies for known vulnerabilities every week and whenever `go.mod` changes.

## Pull requests

### Keep it focused

- Keep each pull request small and about one thing.
- Explain the problem, and why your change fixes it.
- Check that the behaviour doesn't already exist.
- For a change to the TUI, include before and after screenshots.
- For a logic change, say what you tested and how a reviewer can check it.
- If it fixes an issue, say so with `Fixes #123`.

### Keep it brief

Long, AI-generated pull request descriptions and issues will be ignored. Write a short explanation in your own words. If you can't explain the change briefly, the pull request is probably too big.

Using AI tools to help write code is fine, but you need to understand every line you submit and be able to answer questions about it.

### Title it plainly

The title becomes the commit message when the pull request is squash merged. Make it a short, plain summary of the change, like `Skip missing folders during backup`.

## Issues

Before opening an issue, check the docs at [getfro.st/docs](https://getfro.st/docs) and the existing issues. A bug report needs your frost version (`frost --version`), your OS, your storage provider, the command you ran and what it printed. Leave out your recovery phrase and storage credentials. `frost config` masks credentials unless you ask it not to.

## Storage providers

An S3-compatible service needs atomic conditional writes to work through the `s3` backend. [CLI.md](CLI.md#storage-compatibility) lists the requirement and provider findings. If setup fails, open an issue with the provider's name and the error `frost init` shows.

A new storage backend needs an issue first. It implements `storage.Backend` and has to pass `storagetest.Conformance`, as [ARCHITECTURE.md](ARCHITECTURE.md#storage-backends) describes.
