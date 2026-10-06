// Release fixtures shared by the update and security tests:
// a throwaway Ed25519 signing key that writes SSH signatures, and a minimal tar.gz writer.

import { generateKeyPairSync, sign, createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { sshString } from '../../src/platform/signature.js';

// Makes a fresh key and returns its authorized_keys line and a signer. sign() produces the same
// armoured SSHSIG format as `ssh-keygen -Y sign`, using SHA-512 and the "file" namespace by default.
export function signingKey() {
  const { publicKey, privateKey } = generateKeyPairSync('ed25519');
  const pub = publicKey.export({ type: 'spki', format: 'der' }).subarray(-32);
  const key = Buffer.concat([sshString('ssh-ed25519'), sshString(pub)]);
  const authorized = 'ssh-ed25519 ' + key.toString('base64');
  return {
    authorized,
    sign(msg: Buffer, namespace = 'file') {
      const hashAlg = 'sha512';
      const digest = createHash(hashAlg).update(msg).digest();
      const reserved = Buffer.alloc(0);

      // The key signs the magic, namespace, reserved field, hash name and message digest. The
      // armoured blob then wraps the public key, the same fields and that signature.
      const signed = Buffer.concat([Buffer.from('SSHSIG'), ...[namespace, reserved, hashAlg, digest].map(sshString)]);
      const inner = Buffer.concat([sshString('ssh-ed25519'), sshString(sign(null, signed, privateKey))]);
      const version = Buffer.from([0, 0, 0, 1]);
      const blob = Buffer.concat([
        Buffer.from('SSHSIG'),
        version,
        ...[key, namespace, reserved, hashAlg, inner].map(sshString),
      ]);
      return '-----BEGIN SSH SIGNATURE-----\n' + blob.toString('base64') + '\n-----END SSH SIGNATURE-----\n';
    },
  };
}

// Writes a gzipped ustar archive. Each entry gets a 512-byte header, its data padded to a whole
// block, and the archive ends with two empty blocks. `type` defaults to 48 ('0', a regular file).
export function tar(entries: { name: string; data: Buffer; type?: number }[]): Buffer {
  const blocks: Buffer[] = [];
  for (const e of entries) {
    // The header holds the name, mode, uid, gid, octal size and mtime, then the type flag and magic.
    const header = Buffer.alloc(512);
    header.write(e.name);
    header.write('0000755\0', 100);
    header.write('0000000\0', 108);
    header.write('0000000\0', 116);
    header.write(e.data.length.toString(8).padStart(11, '0') + '\0', 124);
    header.write('00000000000\0', 136);
    header.fill(32, 148, 156);
    header[156] = e.type ?? 48;
    header.write('ustar\0', 257);
    header.write('00', 263);

    // The checksum is the byte sum with its own field read as spaces, stored as six octal digits.
    const checksum = [...header].reduce((a, b) => a + b, 0);
    header.write(checksum.toString(8).padStart(6, '0') + '\0 ', 148);
    blocks.push(header, e.data, Buffer.alloc((512 - (e.data.length % 512)) % 512));
  }
  blocks.push(Buffer.alloc(1024));
  return gzipSync(Buffer.concat(blocks));
}
