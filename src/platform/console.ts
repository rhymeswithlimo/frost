// Turns on ANSI escape handling in Windows consoles so CLI styles render, and restores the
// original console modes on exit. Other platforms need nothing.
import { loadFFI } from './ffi-loader.js';

export interface ConsoleAPI {
  getStdHandle(which: number): bigint;
  getConsoleMode(handle: bigint): number | undefined;
  setConsoleMode(handle: bigint, mode: number): boolean;
}

interface ConsoleOptions {
  platform?: string;
  stdoutTTY?: boolean;
  stderrTTY?: boolean;
  api?: ConsoleAPI;
}

export interface ConsoleState {
  ansiOK: boolean;
  restore(): void;
}

let native: ConsoleAPI | undefined;

// kernel32 console calls, bound once. getConsoleMode returns undefined for a null or
// INVALID_HANDLE_VALUE handle and for handles that aren't consoles.
function nativeAPI(): ConsoleAPI {
  if (native) return native;
  const functions = loadFFI().dlopen('kernel32.dll', {
    GetStdHandle: { return: 'pointer', arguments: ['uint32'] },
    GetConsoleMode: { return: 'int32', arguments: ['pointer', 'pointer'] },
    SetConsoleMode: { return: 'int32', arguments: ['pointer', 'uint32'] },
  }).functions;
  native = {
    getStdHandle: which => BigInt(functions.GetStdHandle(which >>> 0) ?? 0),
    getConsoleMode: handle => {
      if (!handle || handle === 0xffffffffffffffffn) return undefined;
      const mode = Buffer.alloc(4);
      return functions.GetConsoleMode(handle, mode) ? mode.readUInt32LE() : undefined;
    },
    setConsoleMode: (handle, mode) => !!functions.SetConsoleMode(handle, mode),
  };
  return native;
}

// Sets ENABLE_VIRTUAL_TERMINAL_PROCESSING (4) on stdout and stderr when they're consoles.
// ansiOK turns false if a console refused the mode. restore() undoes only what changed.
export function enableANSI(options: ConsoleOptions = {}): ConsoleState {
  const undo: (() => void)[] = [];
  const state: ConsoleState = {
    ansiOK: true,
    restore: () => {
      for (const restore of undo.splice(0).reverse()) {
        try {
          restore();
        } catch {}
      }
    },
  };

  const output = options.stdoutTTY ?? !!process.stdout.isTTY;
  const error = options.stderrTTY ?? !!process.stderr.isTTY;
  if ((options.platform ?? process.platform) !== 'win32' || (!output && !error)) return state;
  let api: ConsoleAPI;
  try {
    api = options.api ?? nativeAPI();
  } catch {
    state.ansiOK = false;
    return state;
  }

  // -11 and -12 are STD_OUTPUT_HANDLE and STD_ERROR_HANDLE.
  for (const [tty, which] of [
    [output, -11],
    [error, -12],
  ] as const) {
    if (!tty) continue;
    try {
      const handle = api.getStdHandle(which);
      const mode = api.getConsoleMode(handle);
      // termenv treats failed mode queries as a non-console output.
      if (mode === undefined || mode & 4) continue;
      if (!api.setConsoleMode(handle, (mode | 4) >>> 0)) {
        state.ansiOK = false;
        continue;
      }
      undo.push(() => {
        api.setConsoleMode(handle, mode);
      });
    } catch {
      state.ansiOK = false;
    }
  }
  return state;
}
