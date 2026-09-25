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

Everything uploaded is encrypted. Including file contents, file names, folder structure, sizes and timestamps inside snapshots, and the snapshot list itself. Encryption happens before a single piece of data ever leaves the machine.

The key is never sent anywhere. Not to the storage backend, not to Permafrost, not to us. There's no account and no key escrow.

**If you lose the recovery phrase and the machine, your backups are gone.**

## What a storage provider can see

The provider (an S3 host or Permafrost) can see:

- How many objects you have, their sizes, and when they were uploaded.
- Which objects are chunks, snapshot headers or file lists (from the key prefix).
- When you back up and restore, and from which IP address.
- Snapshot IDs (random words and carry no information).

It can't see *anything* but that just mentioned.

Large files are split into chunks, so their sizes are hidden. A small file is a single chunk, so the provider can see roughly how big it is (after compression), but not what it is or what it's called.

Chunk names are keyed HMACs, not plain hashes, so a provider can't check whether you have a particular known file by hashing it. The keyed chunker stops the same check through chunk size patterns.

Compression before encryption means an object's size depends a little on how compressible its content is. For backups of your own files this reveals very little, but it's not zero.

## Integrity

Every object is authenticated. Decryption fails if a single bit changes, if the wrong key is used, or if an object is moved to a different name (because the name is the associated data). On restore and verify, each chunk's plaintext is also re-hashed and compared with its ID.

So a provider can't make frost restore wrong data. It can only make a restore fail, which frost reports.

The regular spot check re-downloads a random sample of chunks after each backup so missing or damaged data is found early.

## Threat model

**Protected against:**

- A storage provider or Permafrost operator reading your data
- An attacker who gets a copy of the bucket
- Someone on the network between you and the storage (TLS, and every object is authenticated anyway)
- Tampering, truncation or swapping of stored objects (detected, never silently accepted)
- A malicious snapshot trying to make a restore write outside the target directory (paths with `..` are rejected)

**Not protected against:**

- **Someone with access to your machine.** The key file sits unencrypted in your config directory, readable only by your user. It has to be, so scheduled backups can run without you. Anyone who can read it, or run code as you, can read your backups. Use full disk encryption and a locked screen.
- **Losing data.** A provider can delete or withhold your objects. frost will notice (verification fails, restores fail) but can't stop it. Keep a second copy somewhere independent for anything irreplaceable.
- **Rollback.** A provider could hide the newest snapshots and serve only older ones. frost doesn't detect this yet. Each snapshot it does serve is still authentic.
- **Traffic analysis.** Backup timing and sizes are visible, as listed above.
- **A compromised frost binary.** Install from the official releases. Each release's `checksums.txt` is signed with the maintainer's release key (`checksums.txt.sig`), and the install script checks that signature against the public key in the repository (`install/release-signing.pub`) before checking the archive against the checksums. That catches corrupted, swapped or tampered downloads. It can't help if the release key itself is stolen, or if someone can change the install script in the repository.

## Where things live

| File | Contains | Permissions |
|---|---|---|
| `~/.config/frost/key` | Your recovery phrase, in plain text | `0600` |
| `~/.config/frost/config.toml` | Settings and storage credentials | `0600` |
| `~/.cache/frost/manifest-*.db` | Chunk IDs, and your file paths with sizes and mtimes | `0600` |

The manifest holds file paths in plain text, same as your file system does. It never leaves the machine.

On Windows these files are protected by your user profile's default permissions rather than Unix modes.

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
