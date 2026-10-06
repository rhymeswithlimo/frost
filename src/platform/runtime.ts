// Describes the pinned runtime, release targets and installed layout. An app root holds
// runtime/bin/node, launch.mjs, current.json and versions/<version>/.
import os from 'node:os';
import path from 'node:path';

export const nodeVersion = 'v26.10.0';
export const targets = [
  'darwin/amd64',
  'darwin/arm64',
  'linux/amd64',
  'linux/arm64',
  'windows/amd64',
  'windows/arm64',
] as const;

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
