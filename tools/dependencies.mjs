// Checks the installed npm dependencies, file by file, against the sha256 hashes in tools/dependency-files.json.
// `npm run audit:dependencies` runs it, and package.mjs runs it before packaging. An optional folder argument audits
// another project, which tools.test.ts uses.
//
// `--record` instead downloads every package in package-lock.json, checks its sha512 integrity and records each
// file's hash. Run it by hand after changing dependencies. It goes online.
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { gunzipSync } from 'node:zlib';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { readTar, isFile } from './archives.mjs';

const project = fileURLToPath(new URL('../', import.meta.url));
const recordFile = new URL('./dependency-files.json', import.meta.url);

// Only these packages have been reviewed. Any other dependency, even a transitive one, fails until it's added here.
const allowed = new Set(['typescript', '@types/node', 'undici-types', '@iarna/toml', 'prettier']);

const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');

// Audits the dependencies installed in root and returns how many passed. Throws on the first problem.
export async function auditDependencies(root = project) {
  const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
  const reviewed = JSON.parse(await readFile(recordFile, 'utf8'));

  // The lock and the record must name exactly the same packages.
  const locations = Object.keys(lock.packages).filter(Boolean);
  const required = Object.keys(reviewed.packages);
  if (locations.length !== required.length || required.some(location => !locations.includes(location)))
    throw new Error('Dependency lock must contain every reviewed package exactly once');

  let count = 0;
  for (const [location, entry] of Object.entries(lock.packages)) {
    // The empty location is the project itself.
    if (!location) continue;
    const name = location.split('node_modules/').at(-1);
    if (!allowed.has(name)) throw new Error('Dependency needs review: ' + name);

    // The lock must pin a registry archive, and its integrity must be the one the file hashes were recorded from.
    if (!entry.integrity?.startsWith('sha512-') || !entry.resolved?.startsWith('https://registry.npmjs.org/'))
      throw new Error('Unpinned dependency: ' + name);
    if (entry.hasInstallScript) throw new Error('Dependency has an install script: ' + name);
    const pinned = reviewed.packages[location];
    if (!pinned || pinned.integrity !== entry.integrity) throw new Error('Dependency files need review: ' + name);
    const expected = new Map(Object.entries(pinned.files));

    const pkg = JSON.parse(await readFile(path.join(root, location, 'package.json'), 'utf8'));
    if (pkg.gypfile || ['preinstall', 'install', 'postinstall'].some(key => pkg.scripts?.[key]))
      throw new Error('Dependency has a native build: ' + name);

    // Every installed file must be a regular file with its recorded hash, and each match leaves the expected list.
    // Links, special files and compiled binaries are refused.
    async function inspect(dir, prefix = '') {
      for (const e of await readdir(dir, { withFileTypes: true })) {
        const file = path.join(dir, e.name);
        if (e.isSymbolicLink()) throw new Error('Dependency contains a symlink: ' + file);
        if (e.isDirectory()) await inspect(file, prefix + e.name + '/');
        else if (/\.(node|exe|dll|so|dylib|a|lib|wasm)$/i.test(e.name))
          throw new Error('Dependency contains compiled code: ' + file);
        else {
          if (!e.isFile()) throw new Error('Dependency contains a special file: ' + file);
          const relative = prefix + e.name;
          if (sha256(await readFile(file)) !== expected.get(relative))
            throw new Error('Dependency file differs from the pinned archive: ' + file);
          expected.delete(relative);
        }
      }
    }
    await inspect(path.join(root, location));

    // A file still in the expected list was in the archive but isn't installed.
    if (expected.size) throw new Error('Dependency files are missing: ' + name);
    count++;
  }
  return count;
}

// Downloads each locked package, checks it against the lock's integrity and records its files' hashes.
async function recordDependencies() {
  const lock = JSON.parse(await readFile(path.join(project, 'package-lock.json'), 'utf8'));
  const packages = {};
  for (const [location, entry] of Object.entries(lock.packages)) {
    if (!location) continue;
    if (!entry.resolved?.startsWith('https://registry.npmjs.org/') || !entry.integrity?.startsWith('sha512-'))
      throw new Error('Dependency is not pinned');

    // Download at most 16 MiB, then check the archive before reading it.
    const response = await fetch(entry.resolved, { signal: AbortSignal.timeout(30000) });
    if (!response.ok) throw new Error('Dependency download failed');
    const parts = [];
    let size = 0;
    for await (const bytes of response.body) {
      size += bytes.length;
      if (size > 16 << 20) throw new Error('Dependency archive exceeds limit');
      parts.push(Buffer.from(bytes));
    }
    const compressed = Buffer.concat(parts);
    if ('sha512-' + createHash('sha512').update(compressed).digest('base64') !== entry.integrity)
      throw new Error('Dependency differs from its pinned integrity');

    // npm archives keep everything under one top folder, usually package/, so that's dropped. Regular files are
    // recorded and folders ('5') skipped. Links and anything else are refused.
    const files = {};
    for (const file of readTar(gunzipSync(compressed, { maxOutputLength: 128 << 20 }))) {
      const relative = file.name.slice(file.name.indexOf('/') + 1);
      if (isFile(file)) {
        if (
          !relative ||
          relative.startsWith('/') ||
          relative.split('/').some(part => !part || part === '..' || part === '.') ||
          relative.includes('\\') ||
          Object.hasOwn(files, relative)
        )
          throw new Error('Unsafe dependency file');
        files[relative] = sha256(file.data);
      } else if (file.type !== 53) throw new Error('Dependency contains a link or special file');
    }
    if (!Object.hasOwn(files, 'package.json')) throw new Error('Dependency package metadata is missing');
    packages[location] = { integrity: entry.integrity, files };
  }
  await writeFile(recordFile, JSON.stringify({ schema: 1, packages }, null, 2) + '\n');
  console.log('Pinned dependency archives authenticated and file digests recorded.');
}

// Run only as a script, not when package.mjs imports this module.
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv[2] === '--record') await recordDependencies();
  else {
    const count = await auditDependencies(process.argv[2] ? path.resolve(process.argv[2]) : project);
    console.log(`${count} pinned dependencies audited; no native add-ons, executables, or install scripts.`);
  }
}
