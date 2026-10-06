// Prints the release targets the runtime lock pins, separated by spaces. release.sh and CI loop over them, so a
// target gets a package exactly when the lock reviews a runtime for it.
import { readFile } from 'node:fs/promises';

const lock = JSON.parse(await readFile(new URL('./runtime-lock.json', import.meta.url), 'utf8'));
console.log(Object.keys(lock.artifacts).join(' '));
