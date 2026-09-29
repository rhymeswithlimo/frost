# CLI reference

Every command accepts `--config-dir <dir>` to use a different config directory, and `-h` for help.

## `frost init`

Interactive setup. In a terminal it opens a full-screen setup that asks one thing at a time:

| Step | What it asks |
|---|---|
| Storage | Permafrost (just an access key, or get one in the browser), Backblaze B2, Amazon S3, Cloudflare R2, Wasabi, or any other S3-compatible service. Each question says where to find the answer. |
| Folders | Full paths (`~` works). Nothing is picked for you. The same folder, or one inside a folder already on the list, isn't added twice. A folder that doesn't exist yet is skipped until it does. |
| Skip | Names or patterns to leave out. Starts with the `exclude` defaults, which you can remove. |
| Schedule | How often to back up automatically, or off. |
| Recovery phrase | New storage: your key's 24 words, hidden until you press `[v]`, then two of them to check your copy. Storage with backups: the phrase for those backups. |
| Review | Everything on one screen. Change any line, then press `[s]` to save. |

It connects after the storage step and checks it can write there. If that fails, it goes back to the answer that caused it and keeps the rest. If the key on this machine doesn't open the backups already in the storage, it asks for their recovery phrase.

Picking Permafrost asks whether you have an access key. If you don't, frost opens a page in your browser to get one and saves the key to `config.toml` as soon as it comes back, so quitting setup doesn't lose it. If that doesn't work you can try again or paste the key yourself. How the handoff works: [PERMAFROST.md](PERMAFROST.md#getting-a-key).

Saving writes `config.toml` and the key file, and installs the scheduled job. Run it again any time to review and change your settings.

The full-screen setup doesn't ask for a Permafrost server or a folder inside an S3 bucket, and keeps whatever's already set. Use `frost config set storage.permafrost.url` or `storage.s3.prefix` for those.

With piped input it asks plain questions, one per line, with a generic S3 option instead of the provider presets. Getting a Permafrost key works there too.

If Permafrost ever rejects your access key, every command stops with an error saying so. Run `frost init` again to set up a working one.

## `frost backup`

Backs up the configured directories now.

| Flag | Does |
|---|---|
| `-n`, `--dry-run` | Lists new data without uploading objects or saving a snapshot. Reuses unchanged file entries and refreshes the local chunk-presence cache |
| `--path <dir>` | Back up this directory instead of the configured ones. Repeatable |
| `--exclude <pattern>` | Also skip this pattern for this run. Repeatable |
| `--no-verify` | Skip the post-backup spot check |

Files and folders inside a backed-up directory that can't be read (permissions, vanished mid-run) are skipped and listed. The snapshot is still saved, and `status` shows how many were skipped. A configured directory that isn't there (an unplugged drive, a moved folder) is skipped, and `backup` and `status` name it. If none are there, or one exists but can't be read, the whole backup fails rather than quietly saving nothing.

On macOS, protected folders like `~/Documents` need Full Disk Access for the `frost` binary: System Settings > Privacy & Security > Full Disk Access. Scheduled runs need it too.

## `frost restore [snapshot] [paths...]`

Restores files from a snapshot.

**Picking a snapshot:**

| You type | You get |
|---|---|
| `latest` | The newest snapshot |
| `maple-otter-3f1c` or `maple` | That ID, or the only ID starting with that |
| `3 days ago`, `12h`, `2w`, `1 month ago` | The newest snapshot at or before that time |
| `yesterday`, `today` | The newest snapshot by the end of that day |
| `2026-09-20`, `2026-09-20 14:30` | The newest snapshot at or before that date or minute (local time) |

**Paths** limit the restore to those files or folders. Without paths, the whole snapshot is restored.

**Where to** is required, exactly one of:

| Flag | Browser option | Does |
|---|---|---|
| `--beside` | Restore to original location | A new `frost-restore-<id>` folder next to the originals |
| `--to <dir>` | Restore to new location | A new `frost-restore-<id>` folder inside `<dir>`, which must exist |
| `--overwrite` | Overwrite original files | Back where they came from, replacing what's there. Asks first |
| `-y`, `--yes` | | Don't ask before overwriting |

In a new folder, what you restore keeps its own name, relative to the folder the selection shares:

| You restore | `--beside` gives |
|---|---|
| `~/Documents/taxes` | `~/Documents/frost-restore-<id>/taxes/...` |
| `~/notes.txt` | `~/frost-restore-<id>/notes.txt` |
| `~/Documents/a` and `~/Pictures/b` | `~/frost-restore-<id>/Documents/a` and `.../Pictures/b` |

Without paths, the selection is the snapshot's backed-up folders. `--beside` doesn't work when the selection only shares the top of a drive, when that folder isn't on this computer (a snapshot from another machine), or when you can't write to it (backing up your whole home folder puts the new folder in `/Users` or `/home`). Use `--to` then.

The new folder never overwrites anything. If `frost-restore-<id>` is taken, frost uses `-1`, `-2` and so on. Every chunk is decrypted and checked against its ID before it's written, and each file is written to a temp file then renamed, so a failed restore never leaves a half-written file behind. Files restored before a later failure remain restored.

`--overwrite` requires paths from the current OS. It follows links in the folders above a file only when they belong to you or to root (macOS's `/var`, a `~/Dropbox` pointing at another drive); a link owned by anyone else is refused. On Windows any link or junction there is refused. It checks this before asking, and the browser greys out "Overwrite original files" with the reason.

With no arguments in a terminal, `restore` opens the snapshot browser. There, "Restore to new location" opens your system's folder picker (Finder, Explorer, or zenity/qarma on Linux), then shows where everything will land so you can restore, change the location or cancel. Over SSH, or on Linux without one of those, you type the folder instead. Press `[t]` while the picker is open to type it anyway.

## `frost status`

Shows the version, the repository and key fingerprint, how updates are set up, the last backup and whether it worked, when the next one is due, the latest verification result, and the 10 most recent snapshots.

| Flag | Does |
|---|---|
| `--verify` | Run a fresh verification first |
| `-a`, `--all` | List every snapshot |

Put `frost status --verify` in your own scheduler if you want verification on a separate cadence from backups.

## `frost browse`

Opens the snapshot browser.

| Screen | Keys |
|---|---|
| Home | `[enter]` browse snapshots, `[r]` refresh |
| Snapshots | `[enter]` open, `[d]` compare with the previous snapshot, `[m]` mark one then `[d]` on another to compare those two |
| Files | `[enter]` open folder, `[←]` up, `[space]` select, `[a]` select all here, `[c]` clear, `[r]` restore selection (or the highlighted item) |
| Everywhere | `[h]` help, `[s]` settings, `[v]` show or hide the key fingerprint (hidden by default), `[esc]` back, `[q]` quit |

Arrow keys or `j`/`k` move, `pgup`/`pgdn` page, `g`/`G` jump to top and bottom.

After a successful restore, the browser opens the result in Finder, Explorer or your Linux file manager: a single file is shown selected (on Linux, its folder opens), otherwise the deepest folder holding everything restored. Nothing opens over SSH, without a display, or when the restore fails.

## `frost update`

Installs the latest frost release over the binary you ran.

| Flag | Does |
|---|---|
| `--check` | Only say whether there's a newer release |

It downloads the release's `checksums.txt` and `checksums.txt.sig`, checks the signature against the release key built into frost, downloads the archive for your platform and checks its SHA-256. Then it writes the new binary next to the old one, runs it once with `--version`, and renames it into place. If any step fails, the old binary is untouched. Config, key and backups aren't touched either.

Pre-releases are never installed, and nothing older than what you're running is either.

It can't update:

| When | Do instead |
|---|---|
| frost was built from source (`go install`, `go build`) | Rebuild it, or use the installer |
| Homebrew, Nix, Snap, Scoop or a system package installed it | Update it with that |
| You can't write to the folder it's in, e.g. a root-owned `/usr/local/bin` | `sudo frost update`, or reinstall somewhere you can write to |

### Automatic updates

After a scheduled backup, frost checks for a new release at most once a day and installs it the same way. The check runs whether or not the backup worked, and a failed check or install never fails the backup. It's logged to the scheduled run log and shown in `frost status` and the browser's settings.

With `update.auto` set to `false`, the check still runs and `frost status` says when a release is out, but nothing is installed until you run `frost update`. Without scheduled backups there's no background check at all.

On macOS, Full Disk Access may need turning off and on again for the new binary. If a backup fails with "operation not permitted" soon after an update, the error says so.

## `frost config`

| Usage | Does |
|---|---|
| `frost config` | Print every setting. Credentials are masked, `--show-secrets` shows them |
| `frost config get <key>` | Print one setting. Lists print one item per line. Secrets require `--show-secrets` |
| `frost config set <key> <value...>` | Change one setting. Lists take one value per item |
| `frost config edit` | Open `config.toml` in `$VISUAL`, `$EDITOR`, nano, vim or vi (Notepad on Windows) |

Changing `schedule.enabled` or `schedule.every` updates the OS scheduled job straight away.

### Settings

| Key | Default | Meaning |
|---|---|---|
| `paths` | | Directories to back up |
| `exclude` | `.DS_Store`, `Thumbs.db`, `*.tmp`, `*.swp`, `node_modules`, `.cache` | A bare name matches anywhere. A pattern with a `/` matches that path and everything under it |
| `schedule.enabled` | `true` | Run backups automatically |
| `schedule.every` | `daily` | `hourly`, `2h`, `3h`, `4h`, `6h`, `8h`, `12h`, `daily` or `weekly` |
| `verify.sample` | `20` | Chunks re-downloaded and checked after each backup. `0` turns it off |
| `update.auto` | `true` | Install new releases after scheduled backups. `false` only tells you about them |
| `storage.backend` | | `s3` or `permafrost` |
| `storage.s3.endpoint` | | e.g. `s3.us-east-1.amazonaws.com`. A full `https://` URL also works |
| `storage.s3.region` | | Blank if your provider doesn't use one |
| `storage.s3.bucket` | | Must already exist |
| `storage.s3.prefix` | | Optional folder inside the bucket |
| `storage.s3.access_key_id` | | |
| `storage.s3.secret_access_key` | | |
| `storage.s3.insecure` | `false` | Plain HTTP. Only for local testing |
| `storage.permafrost.url` | | Blank means the default server. Otherwise `https://` (or `http://localhost`) |
| `storage.permafrost.token` | | |

### Environment variables

| Variable | Overrides |
|---|---|
| `FROST_S3_ACCESS_KEY_ID`, `AWS_ACCESS_KEY_ID` | `storage.s3.access_key_id` |
| `FROST_S3_SECRET_ACCESS_KEY`, `AWS_SECRET_ACCESS_KEY` | `storage.s3.secret_access_key` |
| `FROST_PERMAFROST_TOKEN` | `storage.permafrost.token` |
| `FROST_CONFIG_DIR` | Config directory, same as `--config-dir` |
| `FROST_CACHE_DIR` | Cache directory |

Values from the environment are never written back into `config.toml`.

## `frost key <show | verify | import>`

| Action | Does |
|---|---|
| `show` | Prints the recovery phrase after you type `show` to confirm |
| `verify` | You type a phrase. frost says whether it's valid, whether it matches this machine's key, and whether it opens your repository |
| `import` | Puts an existing phrase on this machine, e.g. after a reinstall. Checks it against your repository first if one is configured |

## Files

| What | macOS and Linux | Windows |
|---|---|---|
| Config | `~/.config/frost/config.toml` | `%AppData%\frost\config.toml` |
| Key | `~/.config/frost/key` | `%AppData%\frost\key` |
| Manifest (cache) | `~/.cache/frost/manifest-<repo>.db` | `%LocalAppData%\frost\manifest-<repo>.db` |
| Scheduled run log | `~/.cache/frost/frost.log` | `%LocalAppData%\frost\frost.log` |
| Update check | `~/.cache/frost/update.json` | `%LocalAppData%\frost\update.json` |

`XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected on macOS and Linux.

## Scheduled jobs

| OS | Scheduler | Where |
|---|---|---|
| macOS | launchd | `~/Library/LaunchAgents/io.github.rhymeswithlimo.frost.plist` |
| Linux with systemd | systemd user timer | `~/.config/systemd/user/frost-backup.{service,timer}` |
| Linux without systemd | cron | A line in your crontab tagged `# frost-backup` |
| Windows | Task Scheduler | Task named `frost backup` |

The job runs `frost backup --scheduled`, which logs plain lines instead of a progress bar, then checks for updates (see [automatic updates](#automatic-updates)). The systemd timer is `Persistent`, and launchd's interval timer catches up after sleep, so a laptop that was closed runs the missed backup when it wakes. Cron doesn't catch up.

## Exit codes

`0` on success, `1` on any error. A backup whose verification finds a problem also exits `1`.
