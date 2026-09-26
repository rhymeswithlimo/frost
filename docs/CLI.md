# CLI reference

frost has seven commands. Every command accepts `--config-dir <dir>` to use a different config directory, and `-h` for help.

## `frost init`

Interactive setup. In a terminal it opens a full-screen setup that asks one thing at a time:

| Step | What it asks |
|---|---|
| Storage | Permafrost (just an access key), Backblaze B2, Amazon S3, Cloudflare R2, Wasabi, or any other S3-compatible service. Each question says where to find the answer. |
| Folders | Full paths (`~` works). Nothing is picked for you. The same folder, or one inside a folder already on the list, isn't added twice. A folder that doesn't exist yet is skipped until it does. |
| Schedule | How often to back up automatically, or off. |
| Recovery phrase | New storage: your key's 24 words, hidden until you press `[v]`, then two of them to check your copy. Storage with backups: the phrase for those backups. |
| Review | Everything on one screen, including the files to skip. Change any line, then save. |

It connects after the storage step and checks it can write there. If that fails, it goes back to the answer that caused it and keeps the rest. If the key on this machine doesn't open the backups already in the storage, it asks for their recovery phrase.

Saving writes `config.toml` and the key file, and installs the scheduled job. Run it again any time to review and change your settings.

The full-screen setup doesn't ask for a Permafrost server or a folder inside an S3 bucket, and keeps whatever's already set. Use `frost config set storage.permafrost.url` or `storage.s3.prefix` for those.

With piped input it asks plain questions, one per line, with a generic S3 option instead of the provider presets.

## `frost backup`

Backs up the configured directories now.

| Flag | Does |
|---|---|
| `-n`, `--dry-run` | Reads and chunks everything, then lists each file with new data and the total that would upload. Uploads nothing, saves nothing |
| `--path <dir>` | Back up this directory instead of the configured ones. Repeatable |
| `--exclude <pattern>` | Also skip this pattern for this run. Repeatable |
| `--no-verify` | Skip the post-backup spot check |

Files and folders inside a backed-up directory that can't be read (permissions, vanished mid-run) are skipped and listed. The snapshot is still saved, and `status` shows how many were skipped. If a configured directory itself can't be read, the whole backup fails rather than quietly saving nothing.

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

| Flag | Does |
|---|---|
| `-t`, `--target <dir>` | Restore into this directory. Default: `./frost-restore-<id>` |
| `--in-place` | Restore over the original locations. Asks first |
| `-y`, `--yes` | Don't ask |

Files land under the target at their full original path, e.g. `frost-restore-maple-otter-3f1c/Users/me/Documents/report.pdf`. Every chunk is decrypted and checked against its ID before it's written, and each file is written to a temp file then renamed, so a failed restore never leaves a half-written file behind.

With no arguments in a terminal, `restore` opens the snapshot browser.

## `frost status`

Shows the repository and key fingerprint, the last backup and whether it worked, when the next one is due, the latest verification result, and the 10 most recent snapshots.

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

## `frost config`

| Usage | Does |
|---|---|
| `frost config` | Print every setting. Credentials are masked, `--show-secrets` shows them |
| `frost config get <key>` | Print one setting. Lists print one item per line |
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

`XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected on macOS and Linux.

## Scheduled jobs

| OS | Scheduler | Where |
|---|---|---|
| macOS | launchd | `~/Library/LaunchAgents/io.github.rhymeswithlimo.frost.plist` |
| Linux with systemd | systemd user timer | `~/.config/systemd/user/frost-backup.{service,timer}` |
| Linux without systemd | cron | A line in your crontab tagged `# frost-backup` |
| Windows | Task Scheduler | Task named `frost backup` |

The job runs `frost backup --scheduled`, which logs plain lines instead of a progress bar. The systemd timer is `Persistent`, and launchd's interval timer catches up after sleep, so a laptop that was closed runs the missed backup when it wakes. Cron doesn't catch up.

## Exit codes

`0` on success, `1` on any error. A backup whose verification finds a problem also exits `1`.
