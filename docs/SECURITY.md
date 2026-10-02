# Security

frost encrypts your backups on your machine, with a key only you hold, before anything is uploaded. The storage provider, the Permafrost operator and the frost authors can't read them. This page explains how, and where that protection stops.

## Reporting a vulnerability

Please don't open a public issue. Report it privately on GitHub instead, from the repository's **Security** tab, with **Report a vulnerability**.

Include what you found, how to reproduce it, and what an attacker gains. We'll acknowledge the report within a week, keep you updated, and credit you in the release notes unless you'd rather not be named. Please give us a reasonable chance to ship a fix before going public.

A vulnerability is anything that lets someone other than the key holder read backed-up data or metadata, forge or silently change backups, recover the key, or make a restore write outside its target. CLI bugs that don't touch any of those are ordinary issues.

## Encryption

| Piece | Choice |
|---|---|
| Master key | 256 bits from the OS CSPRNG, generated locally by `frost init` |
| Recovery phrase | The master key as 24 BIP39 words. The phrase encodes the key directly, with no PBKDF2 step |
| Subkeys | HKDF-SHA256 from the master key, one label per purpose |
| Encryption | XChaCha20-Poly1305, with a random 192-bit nonce per object |
| Associated data | The object's storage key, like `chunks/ab/ab12...` |
| Chunk names | HMAC-SHA256 of the plaintext under a dedicated subkey |
| Chunk boundaries | FastCDC with a gear table derived from the key |
| Compression | zstd, before encryption, only when it makes the data smaller |

Everything inside a backup object is encrypted, including file contents, file names and snapshot metadata. Object names, snapshot IDs, ciphertext sizes and storage listings are visible to the provider. Setup also writes and deletes a small plaintext probe object to check the storage works, and it holds no file data.

The key is never sent to the storage provider, Permafrost or the frost authors, and there's no key escrow. Storage services use their own credentials and may need an account.

**If you lose the recovery phrase and the machine, your backups are gone.**

## What a storage provider can see

The provider (an S3 host or Permafrost) can see:

- How many objects you have, how big they are, and when they were uploaded.
- Which objects are chunks, snapshot headers or file-list indexes, from the key prefix. The file lists themselves are stored as chunks.
- When you back up and restore, and from which IP address.
- How much new data each backup uploads, which hints at how much changed.
- Snapshot IDs, which are random and carry no information.

A small file fits in a single chunk, so the provider can see roughly how big it is after compression, but not what it is or what it's called. A large file is split into chunks of varying size, so it never shows up as one object of its size, though a backup's total upload is visible.

Chunk names are keyed HMACs, not plain hashes, so a provider can't hash a known file to check whether you have it. The keyed chunker stops the same check through patterns of chunk sizes.

Compressing before encrypting means an object's size depends a little on how compressible its content is. For backups of your own files this reveals very little, but it isn't nothing.

## Integrity

Every object is authenticated. Decryption fails if a single bit changes, if the wrong key is used, or if an object is moved to a different name, because the name is the associated data. On restore and verify, each chunk's plaintext is also hashed again and compared with its ID.

Authentication stops a provider forging content without the key. It doesn't stop a provider deleting or withholding objects.

The spot check after each backup downloads a random sample of chunks. It catches missing or damaged data early, but damage to chunks it didn't sample can go unnoticed.

## Threat model

frost protects against:

- A storage provider or Permafrost operator reading your data.
- An attacker who gets a copy of the bucket.
- Someone on the network between you and the storage. Connections use TLS, and every object is authenticated anyway. TLS is off only when you ask for plain HTTP, with `storage.s3.insecure`, an `http://` S3 endpoint, or an `http://` Permafrost URL on localhost.
- Tampering, truncation or swapping of stored objects, which frost detects and never silently accepts.
- A restore escaping its target through `..` paths or symlinked parents. Restores write through confined directory handles and check every path first.
- An in-place restore being redirected by another user's link on the path to your files. Links owned by root or by you are followed. On Windows, no link is.
- Corrupted, swapped or tampered frost downloads. Each release's `checksums.txt` is signed with the release key (`checksums.txt.sig`). The install script checks that signature against the public key in the repository (`install/release-signing.pub`), then checks the archive against the checksums. `frost update` does the same, as [Updates](#updates) describes.

frost doesn't protect against:

- **Someone with access to your machine.** The key file sits unencrypted in your config directory, readable only by your user, so scheduled backups can run without you. Anyone who can read it, or run code as you, can read your backups. Use full disk encryption and a screen lock.
- **Losing data.** A provider can delete or withhold your objects. Verification can detect this, and restoring the affected files fails, but frost can't prevent it. Keep a second, independent copy of anything irreplaceable.
- **Rollback.** A provider could hide the newest snapshots and serve only older ones. A machine that has already seen those snapshots reports them missing once, but a new machine, or one that never saw them, can't tell. Every snapshot that is served is still authentic.
- **Traffic analysis.** Backup timing and sizes are visible, as listed above.
- **An install without `ssh-keygen`.** The install script skips the signature check, with a warning, when `ssh-keygen` is missing or older than OpenSSH 8.1. It still checks the archive against `checksums.txt`, but that only proves the download matches what the server sent.

## Updates

`frost update`, and scheduled backups unless `update.auto` is `false`, replace the frost binary with the latest release. A release is only installed if it passes every check:

| Check | Stops |
|---|---|
| `checksums.txt.sig` is an SSH signature, namespace `file`, by the release key compiled into frost | A tampered or swapped release, or a hijacked GitHub account or CDN |
| The archive's SHA-256 matches its line in the signed `checksums.txt` | A tampered or corrupted download |
| The archive's name in that file carries the release's version, and that version is newer than yours | Rolling you back to an older signed release. Pre-releases are never picked |
| The new binary runs and reports that version | Installing something that won't start |

The signature check is built into frost and fails closed. Unlike the install script, it never falls back to checking the checksums alone. Only the `frost` binary is taken from the archive, and every download has a size cap.

The binary is replaced with a rename, so it's never half written. On Windows, the running `.exe` is moved aside and deleted on a later run.

Set `update.auto` to `false` to review each release before installing it. frost still tells you when one is out.

## Getting a Permafrost key in setup

The browser hands the key back to frost on `127.0.0.1`, and frost checks it against a random `state`. [PERMAFROST.md](PERMAFROST.md#getting-a-key) has the details.

## Where things live

| File | Holds | Permissions |
|---|---|---|
| `~/.config/frost/key` | Your recovery phrase, in plain text | `0600` |
| `~/.config/frost/config.toml` | Settings and storage credentials | `0600` |
| `~/.cache/frost/manifest-*.db` | Chunk IDs, and your file paths with sizes and mtimes | `0600` |
| `~/.cache/frost/storage-*.json` | Where your backups last opened (storage settings without credentials) and the repository ID | `0600` |
| `~/.cache/frost/update.json` | When updates were last checked, the newest release seen, and the last error | `0600` |
| `~/.cache/frost/frost.log` | Output of scheduled runs with launchd, cron or Task Scheduler, including paths that couldn't be read. [CLI.md](CLI.md#files) lists the Windows path | Set by the scheduler on macOS and Linux; user profile permissions on Windows |

The manifest holds file paths in plain text, as your file system does, and it never leaves the machine. On Windows, these files are protected by your user profile's default permissions rather than Unix modes.

Restore replaces files one at a time, after checking their data and size. If a later file fails, earlier replacements stay. Restored symlinks keep their original targets, which can point outside the restore folder.

## Verifying a download by hand

The install script does this for you. To check an archive yourself, download it with `checksums.txt` and `checksums.txt.sig` from the same release, and [`install/release-signing.pub`](../install/release-signing.pub) from the repository. Then run:

```sh
printf 'frost-release %s\n' "$(cat release-signing.pub)" > allowed_signers
ssh-keygen -Y verify -f allowed_signers -I frost-release -n file -s checksums.txt.sig < checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
```

The first check should print `Good "file" signature`, and the second `OK` for your archive.

## Recommendations

- Keep the recovery phrase offline, on paper or in a password manager you trust.
- Run `frost key verify` now and then to make sure the phrase you wrote down is the right one.
- Give the S3 credentials access to one bucket only.
- Watch `frost status`, and look into a health problem straight away.
