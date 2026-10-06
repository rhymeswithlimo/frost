// Runs external programs without a shell. Schedulers, desktop actions, audio and the updater
// take a Runner so tests can inject a fake one instead of touching the real system.
import { spawn } from 'node:child_process';

interface CommandResult {
  code: number;
  stdout: string;
  stderr: string;
}

export type Runner = (
  program: string,
  args: string[],
  input?: string | Buffer,
  signal?: AbortSignal,
) => Promise<CommandResult>;

// The default runner. Output is capped at 8 MiB across stdout and stderr, and a missing
// exit code (killed by a signal) counts as 1.
export const run: Runner = (program, args, input, signal) =>
  new Promise((resolve, reject) => {
    const env = { ...process.env };
    // Started from PowerShell 7, Windows PowerShell would inherit its module path and load incompatible modules.
    if (/^(?:.*[\\/])?powershell(?:\.exe)?$/i.test(program)) delete env.PSModulePath;
    const child = spawn(program, args, { windowsHide: true, shell: false, signal, stdio: 'pipe', env });

    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    let bytes = 0;
    const collect = (dest: Buffer[], data: Buffer) => {
      bytes += data.length;
      if (bytes > 8 * 1024 * 1024) {
        child.kill();
        reject(new Error('command output too big'));
      } else dest.push(data);
    };
    child.stdout.on('data', data => collect(stdout, data));
    child.stderr.on('data', data => collect(stderr, data));
    child.on('error', reject);
    child.on('close', code =>
      resolve({ code: code ?? 1, stdout: Buffer.concat(stdout).toString(), stderr: Buffer.concat(stderr).toString() }),
    );

    // A child that exits without reading its input makes stdin fail with EPIPE. That's fine.
    child.stdin.on('error', () => {});
    child.stdin.end(input);
  });

// Runs a command and returns its stdout, or throws with its stderr (or stdout) on a non-zero exit.
export async function checked(
  runner: Runner,
  program: string,
  args: string[],
  input?: string | Buffer,
): Promise<string> {
  const result = await runner(program, args, input);
  if (result.code) throw new Error(`${program}: ${(result.stderr || result.stdout).trim()}`);
  return result.stdout;
}
