// Describes the installed layout. An app root holds runtime/bin/node, launch.mjs, current.json and
// versions/<version>/. tools/runtime-lock.json owns the runtime version and the release targets.
import os from 'node:os';
import path from 'node:path';

// The default per-user app root on each platform.
export function installationRoot(platform = process.platform, env = process.env, home = os.homedir()): string {
  if (platform === 'win32') return path.join(env.LOCALAPPDATA || path.join(home, 'AppData', 'Local'), 'frost', 'app');
  if (platform === 'darwin') return path.join(home, 'Library', 'Application Support', 'frost', 'app');
  return path.join(env.XDG_DATA_HOME || path.join(home, '.local', 'share'), 'frost', 'app');
}

// The bundled runtime stays at this fixed path across updates.
export function runtimePath(root: string, platform = process.platform): string {
  return path.join(root, 'runtime', 'bin', platform === 'win32' ? 'node.exe' : 'node');
}
