# CLI reference

frost has eight commands. Every one takes `--config-dir <dir>` to use a different config directory, and `-h` (`--help`) to show its help. `frost --version` prints the version.

## `frost init`

In a terminal, `frost init` opens a full-screen setup that asks one thing at a time:

| Step | What it asks |
|---|---|
| Storage | Permafrost (an access key, or get one in the browser), or an [S3-compatible service](#storage-compatibility). Presets fill in provider-specific questions |
| Folders | Full paths (`~` works). Nothing is picked for you. A folder that's already on the list, or inside one that is, isn't added twice. A folder that doesn't exist yet is skipped until it does |
| Skip | Names or patterns to leave out. Starts with the `exclude` defaults, which you can remove |
| Schedule | How often to back up automatically, or off |
| Recovery phrase | For new storage, your key's 24 words, hidden until you press `[v]`, then two of them to check your copy. For storage that already has backups, the phrase for those backups |
| Review | Everything on one screen. Change any line, then press `[s]` to save |

Setup connects right after the storage step and checks it can read, list, write and delete a test object, including [conditional writes](#storage-compatibility). If that fails, it goes back to the answer that caused it and keeps the rest. If the key on this machine doesn't open the backups already in that storage, it asks for their recovery phrase. If this machine's backups are somewhere else and the new storage is empty, it asks before starting a separate set of backups there.

Picking Permafrost asks whether you have an access key. If you don't, frost opens a page in your browser to get one. The key comes back to frost and is saved to `config.toml` straight away, so quitting setup doesn't lose it. If that doesn't work, you can try again or paste the key yourself. [PERMAFROST.md](PERMAFROST.md#getting-a-key) explains the handoff.

Saving writes `config.toml` and the key file and installs the scheduled job. Run `frost init` again any time to review or change your settings.

The full-screen setup doesn't ask for a Permafrost server or a folder inside an S3 bucket, and keeps whatever's already set. Use `frost config set storage.permafrost.url` or `storage.s3.prefix` for those.

With piped input, `init` asks plain questions, one per line. It offers a generic S3 option instead of the provider presets and asks for the folder inside the bucket. Getting a Permafrost key works there too.

If Permafrost ever rejects your access key, every command stops with an error that says so. Run `frost init` again to set up a working one.

### Storage compatibility

frost needs atomic conditional PUTs (`If-None-Match: *`) to prevent machines from overwriting each other's backup metadata. Setup checks that a second create fails without changing the original object. A preset saves typing; it doesn't certify a provider. frost also checks a new storage location before accepting it through `frost config set`.

| Provider | Conditional PUT support |
|---|---|
| Amazon S3 | [Documented](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html) |
| Cloudflare R2 | [Documented](https://developers.cloudflare.com/r2/api/s3/api/) |
| MinIO | [Implemented in the server](https://github.com/minio/minio/blob/master/cmd/object-handlers.go). Your installed version must pass setup's check |
| Backblaze B2 | Unverified. Its [PUT reference](https://www.backblaze.com/apidocs/s3-put-object) doesn't list `If-None-Match` |
| Wasabi | Unverified. Its [API reference](https://docs.wasabi.com/apidocs/operations-on-objects) doesn't establish support for conditional PUTs |
| Garage | Unsupported, according to its [maintainer](https://news.ycombinator.com/item?id=46329908) |

These findings were checked against documentation and source on 2 October 2026, without live cloud-account tests. Setup refuses storage that rejects or ignores conditional creates. Changing credentials can't fix missing provider support.

## `frost backup`

Backs up the configured folders now. Only data that changed since the last run is uploaded.

| Flag | Does |
|---|---|
| `-n`, `--dry-run` | List the files with new data and the total, without uploading anything or saving a snapshot |
| `--path <dir>` | Back up this folder instead of the configured ones. Repeatable |
| `--exclude <pattern>` | Also skip this pattern for this run. Repeatable |
| `--no-verify` | Skip the spot check after the backup |

After a backup, frost downloads `verify.sample` random chunks and checks them. If any check fails, `backup` exits with `1`.

### What's backed up

frost backs up regular files, folders and symlinks, with their permissions and modification times. A symlink is stored as the link itself, and hard-linked files are backed up and restored as separate files. frost skips sockets, devices, pipes and the `.frost-partial-...` files a stopped restore leaves behind, and it doesn't keep owners, ACLs or extended attributes.

### When something goes wrong

| What happens | What frost does |
|---|---|
| A file or folder inside a backed-up folder can't be read (permissions, deleted mid-run) | Skips it and lists it. The snapshot is still saved, and `status` shows how many were skipped |
| A configured folder isn't there (an unplugged drive, a moved folder) | Skips it and backs up the rest. `backup` and `status` name it |
| None of the configured folders are there, or one exists but can't be read | Fails the whole backup, so it doesn't look fine while saving nothing |
| A file changes while it's read (a database in use, a running VM's disk, a download) | Reads it again at the end of the backup. If it's still changing, the snapshot keeps its previous copy, and `backup` and `status` list it. A file that was never backed up cleanly is skipped |

frost doesn't take filesystem or database snapshots. Back up a file that's always busy while it's closed or stopped.

Backups don't list every object in storage. frost trusts its local record of what's uploaded and checks it against storage once a week, after a check finds something missing, or when the storage isn't where it was last checked.

On macOS, protected folders like `~/Documents` need Full Disk Access for the `frost` binary, scheduled runs included. Add it under System Settings > Privacy & Security > Full Disk Access.

## `frost restore [snapshot] [paths...]`

Restores files from a snapshot. With no arguments in a terminal, it opens the [snapshot browser](#frost-browse).

### Picking a snapshot

frost shows a snapshot's ID as two words and 4 more characters, like `maple-absurd-3f1c`. When two IDs would look the same, it shows more characters for both. If an ID you type matches more than one snapshot, frost asks for more of it. The full ID has 11 characters after the words. It names the snapshot's objects in storage, and the command that [carries on a restore](#interrupted-restores) uses it.

| You type | You get |
|---|---|
| `latest` | The newest snapshot |
| `maple-absurd-3f1c` or `maple` | The snapshot with that ID, or the only one whose ID starts with what you typed |
| `3 days ago`, `12h`, `2w`, `1 month ago` | The newest snapshot at or before that time |
| `yesterday`, `today` | The newest snapshot by the end of that day |
| `2026-09-20`, `2026-09-20 14:30` | The newest snapshot at or before that day or minute, in local time |

Paths limit the restore to those files or folders and everything inside them. Without paths, the whole snapshot is restored.

### Where files go

Pick exactly one:

| Flag | In the browser | Does |
|---|---|---|
| `--beside` | New folder beside originals | Restores into a new `frost-restore-<id>` folder next to the originals |
| `--to <dir>` | New folder elsewhere | Restores into a new `frost-restore-<id>` folder inside `<dir>`, which must exist |
| `--overwrite` | Overwrite original files | Restores back where the files came from, replacing what's there. Asks first |

`-y`, `--yes` skips the question before overwriting.

In a new folder, what you restore keeps its own name, relative to the folder the selection shares:

| You restore | `--beside` gives |
|---|---|
| `~/Documents/taxes` | `~/Documents/frost-restore-<id>/taxes/...` |
| `~/notes.txt` | `~/frost-restore-<id>/notes.txt` |
| `~/Documents/a` and `~/Pictures/b` | `~/frost-restore-<id>/Documents/a` and `.../Pictures/b` |

Without paths, the selection is the snapshot's backed-up folders. `--beside` doesn't work when the selection only shares the top of a drive, when that folder isn't on this computer (a snapshot from another machine), or when you can't write to it. Backing up your whole home folder, for example, would put the new folder in `/Users` or `/home`. Use `--to` in those cases.

A new folder never overwrites anything. Its name always uses the ID with 4 characters, and if that name is taken, frost adds `-1`, `-2` and so on.

Every chunk is decrypted and checked against its ID before it's written, and chunks download in parallel. Each file is written to a hidden `.frost-partial-...` file and renamed into place when it's complete, so a failed restore never leaves a half-written file where a real one was.

### Overwriting

`--overwrite` needs a snapshot from a computer with the same kind of paths (macOS and Linux, or Windows). It follows links in the folders above a file only when they belong to you or to root, like macOS's `/var` or a `~/Dropbox` that points at another drive. A link owned by anyone else is refused, and on Windows any link or junction there is refused. frost checks this before asking, and the browser greys out "Overwrite original files" and says why.

Originals that already match the snapshot are checked and skipped, so they aren't downloaded. Overwriting needs room for the new copy of a file next to the old one until it's renamed into place.

### Interrupted restores

If a restore stops (a lost connection, Ctrl+C, the machine sleeping), frost prints the command that carries on. It's the same restore with the snapshot's full ID in place of `latest` or a time, so a backup in between doesn't change which snapshot it means.

The new run carries on in the same folder. Files already there are checked and skipped, and the file it was writing continues from its last good chunk. Until the restore finishes, the folder holds a `.frost-restore` marker and the unfinished file's `.frost-partial-...`. `--overwrite` carries on the same way.

## `frost status`

Shows the version, the storage and key fingerprint, the last backup and whether it worked, when the next one is due, the latest verification result, how updates are set up, and the 10 most recent snapshots.

| Flag | Does |
|---|---|
| `--verify` | Run a fresh verification first. It checks `verify.sample` random chunks (20 if that's `0`) and compares the local chunk list with everything in storage |
| `-a`, `--all` | List every snapshot |

If snapshots this machine knew about have gone from storage, `status` says so once. `status --verify` exits with `1` when verification fails, so you can run it from your own scheduler to verify on a different schedule from backups.

## `frost browse`

Opens the snapshot browser.

| Screen | Keys |
|---|---|
| Home | `[enter]` browse snapshots, `[r]` refresh |
| Snapshots | `[enter]` open, `[d]` compare with the previous snapshot, `[m]` mark one, then `[d]` on another to compare those two |
| Files | `[enter]` open a folder, `[←]` go up, `[space]` select, `[a]` select or unselect everything here, `[c]` clear the selection, `[r]` restore the selection (or the highlighted item) |
| Restore | `[1]` `[2]` `[3]` pick where files go, `[enter]` restore or choose a folder, `[y]` confirm overwriting, `[c]` change the folder, `[esc]` cancel |
| Everywhere | `[h]` help, `[s]` settings, `[v]` show or hide the key fingerprint (hidden by default), `[esc]` back, `[q]` quit |

Arrow keys or `j`/`k` move, `pgup`/`pgdn` page, and `g`/`G` jump to the top and bottom. Help and settings scroll the same way when they don't fit. Long restore confirmations and results scroll with `[pgup pgdn]`, which never starts a restore or dismisses its result.

"New folder elsewhere" opens your system's folder picker (Finder, Explorer, or zenity, qarma or matedialog on Linux), then shows where everything will land so you can restore, change the folder or cancel. Over SSH, or on Linux without a picker, you type the folder instead. Press `[t]` while the picker is open to type it anyway.

After a successful restore, the browser shows the result in Finder, Explorer or your Linux file manager. A single restored file is shown selected (on Linux, its folder opens). Otherwise the deepest folder holding everything restored opens. Nothing opens over SSH, without a display, or when the restore fails.

## `frost update`

Installs the latest frost release over the binary you ran.

| Flag | Does |
|---|---|
| `--check` | Only say whether there's a newer release |

frost downloads the release's `checksums.txt` and `checksums.txt.sig` and checks the signature against the release key built into frost. Then it downloads the archive for your platform, checks its SHA-256, writes the new binary next to the old one, runs it once with `--version`, and renames it into place. If any step fails, the old binary stays as it was. Your config, key and backups aren't touched.

It never installs a pre-release, or anything older than what you're running.

It can't update in these cases:

| When | Do instead |
|---|---|
| frost was built from source (`go install`, `go build`) | Rebuild it, or use the installer |
| Homebrew, Nix, Snap, Scoop or a system package installed it | Update it with that |
| You can't write to the folder it's in, like a root-owned `/usr/local/bin` | Run `sudo frost update`, or reinstall somewhere you can write to |

### Automatic updates

After a scheduled backup, frost checks for a new release at most once a day and installs it the same way. The check runs whether or not the backup worked, and a failed check or install never fails the backup. The result shows in `frost status` and the browser's settings, and in the [scheduled run log](#files) where there is one.

With `update.auto` set to `false`, the check still runs and `frost status` says when a release is out, but nothing is installed until you run `frost update`. Without scheduled backups there's no background check at all.

On macOS, Full Disk Access may need turning off and on again for the new binary. If a backup fails with "operation not permitted" soon after an update, the error says so.

## `frost config`

| Usage | Does |
|---|---|
| `frost config` | Print every setting. Credentials are masked unless you add `--show-secrets` |
| `frost config get <key>` | Print one setting. Lists print one item per line, and secrets need `--show-secrets` |
| `frost config set <key> <value...>` | Change one setting. Lists take one value per item |
| `frost config edit` | Open `config.toml` in `$VISUAL`, `$EDITOR`, nano, vim or vi (Notepad on Windows) |

Changing `schedule.enabled` or `schedule.every`, with `set` or `edit`, updates the OS scheduled job straight away.

### Settings

| Key | Default | Meaning |
|---|---|---|
| `paths` | | Folders to back up |
| `exclude` | `.DS_Store`, `Thumbs.db`, `*.tmp`, `*.swp`, `node_modules`, `.cache` | A bare name or pattern matches anywhere. A pattern with a `/` is a full path (`~` works) and matches that path and everything under it |
| `schedule.enabled` | `true` | Run backups automatically |
| `schedule.every` | `daily` | `hourly`, `2h`, `3h`, `4h`, `6h`, `8h`, `12h`, `daily` or `weekly` |
| `verify.sample` | `20` | Chunks downloaded and checked after each backup. `0` turns the check off |
| `update.auto` | `true` | Install new releases after scheduled backups. `false` only tells you about them |
| `storage.backend` | | `s3` or `permafrost` |
| `storage.s3.endpoint` | | Like `s3.us-east-1.amazonaws.com`. A full `https://` URL also works, and an `http://` one turns off TLS |
| `storage.s3.region` | | Blank if your provider doesn't use one |
| `storage.s3.bucket` | | Must already exist |
| `storage.s3.prefix` | `frost` | The folder inside the bucket that holds everything. Empty for the top level |
| `storage.s3.access_key_id` | | |
| `storage.s3.secret_access_key` | | |
| `storage.s3.insecure` | `false` | Use plain HTTP. Only for local testing |
| `storage.permafrost.url` | | Blank for the default server. Otherwise an `https://` URL (`http://` only for localhost) |
| `storage.permafrost.token` | | |

### Moving your backups

| You want to | Do this |
|---|---|
| Move them to another folder or bucket | Move the whole folder (`frost.repo`, `chunks/`, `snapshots/` and `trees/`), then point frost at it with `frost config set`. Nothing uploads again |
| Start a separate set of backups somewhere else | Run `frost init` and point it at the empty location. The old backups stay where they are, but frost only shows the new ones |
| Go back to backups you moved away from | Set the old location again |

Moving only `frost.repo` doesn't move your backups. `frost config set` checks a new location before saving it, and refuses one with no backups, backups made with another key, or a `frost.repo` without its snapshots. `frost config edit` only warns about these, because your editor has already saved the file. If frost can't find your backups, the error and `frost status` say where they were last opened and how to get back to them.

### Environment variables

| Variable | Overrides |
|---|---|
| `FROST_S3_ACCESS_KEY_ID`, `AWS_ACCESS_KEY_ID` | `storage.s3.access_key_id` |
| `FROST_S3_SECRET_ACCESS_KEY`, `AWS_SECRET_ACCESS_KEY` | `storage.s3.secret_access_key` |
| `FROST_PERMAFROST_TOKEN` | `storage.permafrost.token` |
| `FROST_CONFIG_DIR` | The config directory, same as `--config-dir` |
| `FROST_CACHE_DIR` | The cache directory |

Values from the environment are never written to `config.toml`.

## `frost key <show | verify | import>`

| Action | Does |
|---|---|
| `show` | Prints the recovery phrase after you type `show` to confirm |
| `verify` | Asks for a phrase and says whether it's valid, whether it matches this machine's key, and whether it opens your backups |
| `import` | Puts an existing phrase on this machine, after a reinstall for example. If storage is configured, it checks the phrase opens it first |

## Files

| What | macOS and Linux | Windows |
|---|---|---|
| Config | `~/.config/frost/config.toml` | `%AppData%\frost\config.toml` |
| Key | `~/.config/frost/key` | `%AppData%\frost\key` |
| Manifest (a cache) | `~/.cache/frost/manifest-<repo>.db` | `%LocalAppData%\frost\manifest-<repo>.db` |
| Where backups last opened, one per config folder | `~/.cache/frost/storage-<config>.json` | `%LocalAppData%\frost\storage-<config>.json` |
| Update check | `~/.cache/frost/update.json` | `%LocalAppData%\frost\update.json` |
| Scheduled run log, with launchd, cron and Task Scheduler | `~/.cache/frost/frost.log` | `%LocalAppData%\frost\frost.log` |

On macOS and Linux, frost respects `XDG_CONFIG_HOME` and `XDG_CACHE_HOME`. With systemd, scheduled runs log to the journal instead (`journalctl --user -u frost-backup`). On Windows, frost appends backup output, failures and update results to the log itself, including with tasks installed by an older version. Logs larger than 1 MiB are emptied before the next run.

## Scheduled jobs

| OS | Scheduler | Where |
|---|---|---|
| macOS | launchd | `~/Library/LaunchAgents/io.github.rhymeswithlimo.frost.plist` |
| Linux with systemd | systemd user timer | `~/.config/systemd/user/frost-backup.{service,timer}` |
| Linux without systemd | cron | A line in your crontab tagged `# frost-backup` |
| Windows | Task Scheduler | A task named `frost backup` |

The job runs `frost backup --scheduled`, which logs plain lines instead of a progress bar and then checks for [updates](#automatic-updates). launchd and the systemd timer catch up, so a laptop that was closed runs the missed backup when it wakes. Cron and Task Scheduler skip runs the machine was off or asleep for, and run daily and weekly backups at 03:17.

Jobs keep the config and cache directories used when they're installed, including environment overrides. Run `frost init` again after changing those directories, or to update an older job that didn't keep them.

## Exit codes

frost exits with `0` on success and `1` on any error, including a backup or `status --verify` whose verification finds a problem.
