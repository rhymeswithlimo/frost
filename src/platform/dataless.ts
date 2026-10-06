// macOS downloads a file that's only in iCloud as soon as something reads it. Backups turn that off for the
// whole process, so reading such a file or folder fails with EDEADLK (errno 11) instead and the backup skips it.
import { loadFFI } from './ffi-loader.js';

let refused = false;

// setiopolicy_np sets IOPOL_TYPE_VFS_MATERIALIZE_DATALESS_FILES (3) for IOPOL_SCOPE_PROCESS (0) to
// IOPOL_MATERIALIZE_DATALESS_FILES_OFF (1). It covers every thread, including the ones that read large files.
// Other platforms have no such files, so this does nothing there.
export function refuseDatalessReads(platform = process.platform): void {
  if (platform !== 'darwin' || refused) return;
  const { lib } = loadFFI().dlopen('/usr/lib/libSystem.B.dylib');
  try {
    const set = lib.getFunction('setiopolicy_np', { return: 'int32', arguments: ['int32', 'int32', 'int32'] });
    if (set(3, 0, 1) !== 0) throw new Error("couldn't stop macOS downloading files that are only in iCloud");
    refused = true;
  } finally {
    lib.close();
  }
}
