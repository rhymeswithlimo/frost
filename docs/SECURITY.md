# Security

With frost, we promise nobody but you can read your backups. Not the storage provider, not the Permafrost operator, not the frost authors. This page says exactly how, and where that promise stops.

## Reporting a vulnerability

When reporting a vulnerability, please don't open a public issue. Report it privately through GitHub: go to the repository's **Security** tab and choose **Report a vulnerability**.

Include what you found, how to reproduce it, and what an attacker gains. We'll acknowledge the report within a week, keep you updated, and credit you in the release notes unless you'd rather not be named. Please give us a reasonable chance to ship a fix before going public.

A vulnerability is anything that lets someone other than the key holder read backed-up data or metadata, forge or silently alter backups, recover the key, or make a restore write outside its target. Bugs in the CLI that don't touch those are ordinary issues.

## Encryption model

| Piece | Choice |
|---|---|
| Master key | 256 bits from the OS CSPRNG, generated locally by `frost init` |
| Recovery phrase | The master key as 24 BIP39 words (the phrase encodes the key directly; there's no PBKDF2 step) |
| Subkeys | HKDF-SHA256 from the master key, one label per purpose |
| Encryption | XChaCha20-Poly1305, random 192-bit nonce per object |
| Associated data | The object's storage key, e.g. `chunks/ab/ab12...` |
| Chunk names | HMAC-SHA256 of the plaintext under a dedicated subkey |
| Chunk boundaries | FastCDC with a gear table derived from the key |
| Compression | zstd, before encryption, only when it shrinks the data |

Backup object bodies are encrypted, including file contents and snapshot metadata. Object names, snapshot IDs, ciphertext sizes and storage listings are visible to the provider. Setup also sends a small plaintext connectivity probe, which contains no user file data.

The encryption key isn't sent to the storage backend, Permafrost or the frost authors. There's no key escrow. Storage services use separate credentials and may require an account.

**If you lose the recovery phrase and the machine, your backups are gone.**

## What a storage provider can see

The provider (an S3 host or Permafrost) can see:

- How many objects you have, their sizes, and when they were uploaded.
- Which objects are chunks, snapshot headers or file-list indexes (from the key prefix). File lists themselves are stored as chunks.
- When you back up and restore, and from which IP address.
- Snapshot IDs (random words and carry no information).

Repeated chunk IDs also reveal reuse of the same content within a repository.

Large files are split into chunks, so their sizes are hidden. A small file is a single chunk, so the provider can see roughly how big it is (after compression), but not what it is or what it's called.

Chunk names are keyed HMACs, not plain hashes, so a provider can't check whether you have a particular known file by hashing it. The keyed chunker stops the same check through chunk size patterns.

Compression before encryption means an object's size depends a little on how compressible its content is. For backups of your own files this reveals very little, but it's not zero.

## Integrity

Every object is authenticated. Decryption fails if a single bit changes, if the wrong key is used, or if an object is moved to a different name (because the name is the associated data). On restore and verify, each chunk's plaintext is also re-hashed and compared with its ID.

Authentication prevents a provider from forging new content without the key. It doesn't prevent withholding objects or replaying an older authentic object under the same name.

The regular spot check re-downloads a random sample of chunks after each backup. It can catch missing or damaged data early, but unsampled corruption can remain undetected.

## Threat model

**Protected against:**

- A storage provider or Permafrost operator reading your data
- An attacker who gets a copy of the bucket
- Someone on the network between you and the storage (TLS, and every object is authenticated anyway)
- Tampering, truncation or swapping of stored objects (detected, never silently accepted)
- Restore path traversal and symlink parents under an explicit target, using confined directory handles and path validation
- An in-place restore being redirected by another user's link on the path to your files (links owned by root or by you are followed; on Windows no link is)
- Corrupted, swapped or tampered frost downloads. Each release's `checksums.txt` is signed with the release key (`checksums.txt.sig`). The install script checks that signature against the public key in the repository (`install/release-signing.pub`), then checks the archive against the checksums. `frost update` does the same, see [Updates](#updates)

**Not protected against:**

- **Someone with access to your machine.** The key file sits unencrypted in your config directory, readable only by your user. It has to be, so scheduled backups can run without you. Anyone who can read it, or run code as you, can read your backups. Use full disk encryption and a locked screen.
- **Losing data.** A provider can delete or withhold your objects. Verification can detect this, and restoring affected files fails, but frost can't prevent deletion. Keep a second copy somewhere independent for anything irreplaceable.
- **Rollback.** A provider could hide the newest snapshots and serve only older ones. frost doesn't detect this yet. Each snapshot it does serve is still authentic.
- **Traffic analysis.** Backup timing and sizes are visible, as listed above.

## Updates

`frost update`, and scheduled backups unless `update.auto` is `false`, replace the frost binary with the latest release. A release is only installed if:

| Check | Stops |
|---|---|
| `checksums.txt.sig` is an SSH signature, namespace `file`, by the release key compiled into frost | A tampered or swapped release, a hijacked GitHub account or CDN |
| The archive's SHA-256 matches its line in the signed `checksums.txt` | A tampered or corrupted download |
| The archive's name in that file carries the release's version, and that version is newer than yours | Rolling you back to an older signed release. Pre-releases are never picked |
| The new binary runs and reports that version | Installing something that won't start |

The signature check is built into frost and fails closed. Unlike the install script, it never falls back to checksums only. Only the `frost` binary is taken from the archive, and every download has a size cap.

The binary is replaced with a rename, so it's never half written. On Windows the running `.exe` is moved aside and deleted on a later run.

Set `update.auto` to `false` if you'd rather review each release first: frost still says when one is out.

## Getting a Permafrost key in setup

The browser hands the key back to frost on `127.0.0.1`, checked against a random `state`. See [PERMAFROST.md](PERMAFROST.md#getting-a-key).

## Where things live

| File | Contains | Permissions |
|---|---|---|
| `~/.config/frost/key` | Your recovery phrase, in plain text | `0600` |
| `~/.config/frost/config.toml` | Settings and storage credentials | `0600` |
| `~/.cache/frost/manifest-*.db` | Chunk IDs, and your file paths with sizes and mtimes | `0600` |
| `~/.cache/frost/update.json` | When updates were last checked, the newest release seen, the last error | `0600` |

The manifest holds file paths in plain text, same as your file system does. It never leaves the machine.

On Windows these files are protected by your user profile's default permissions rather than Unix modes.

Restore replaces files individually after checking their data and size. If a later file fails, earlier replacements remain. Restored symlinks retain their original targets, which can point outside the restore directory when you open them later.

## Verifying a download by hand

The install script does this for you. To check an archive yourself, download it with `checksums.txt` and `checksums.txt.sig` from the same release, plus [`install/release-signing.pub`](../install/release-signing.pub) from the repository, then:

```sh
printf 'frost-release %s\n' "$(cat release-signing.pub)" > allowed_signers
ssh-keygen -Y verify -f allowed_signers -I frost-release -n file -s checksums.txt.sig < checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
```

The first command should say `Good "file" signature`, the second `OK` for your archive.

## Recommendations

- Store the recovery phrase offline: on paper, or in a password manager you trust.
- Run `frost key verify` now and then to make sure the phrase you've got written down is the right one.
- Give the S3 credentials access to one bucket only.
- Watch `frost status`. A health problem is worth looking into straight away.
