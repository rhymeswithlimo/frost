// Plays a raw 16-bit mono stream through winmm's waveOut on Windows. JavaScript can't run on the audio thread, so the
// device opens without a callback and finished blocks are found by polling their WHDR_DONE flag. A fixed ring of
// blocks is allocated and prepared once, outside the JavaScript heap, so their addresses never move while the device
// holds them.
import { loadFFI, type FFIType } from './ffi-loader.js';
import { sampleRate } from './sound.js';

// The winmm calls WaveOut makes, and the raw address of a buffer. Tests pass a fake.
export interface WinMM {
  waveOutOpen(handle: Buffer, device: number, format: Buffer, callback: null, instance: null, flags: number): number;
  waveOutPrepareHeader(wave: bigint, header: Buffer, size: number): number;
  waveOutUnprepareHeader(wave: bigint, header: Buffer, size: number): number;
  waveOutWrite(wave: bigint, header: Buffer, size: number): number;
  waveOutReset(wave: bigint): number;
  waveOutClose(wave: bigint): number;
  pointer(buffer: Buffer): bigint;
}

// WAVEHDR is 48 bytes on 64-bit Windows. lpData is at 0, dwBufferLength at 8 and dwFlags at 24, where WHDR_DONE (1)
// marks a played block.
const headerSize = 48;
const done = 1;
const ringSize = 8;

// Binds winmm once. The struct offsets assume a 64-bit Windows ABI.
let native: WinMM | undefined;
function bind(): WinMM {
  if (native) return native;
  if (process.platform !== 'win32' || !['x64', 'arm64'].includes(process.arch))
    throw new Error('unsupported Windows audio ABI');
  const ffi = loadFFI();
  const definition = (...args: FFIType[]) => ({ return: 'uint32' as const, arguments: args });
  const f = ffi.dlopen('winmm.dll', {
    waveOutOpen: definition('pointer', 'uint32', 'pointer', 'pointer', 'pointer', 'uint32'),
    waveOutPrepareHeader: definition('pointer', 'pointer', 'uint32'),
    waveOutUnprepareHeader: definition('pointer', 'pointer', 'uint32'),
    waveOutWrite: definition('pointer', 'pointer', 'uint32'),
    waveOutReset: definition('pointer'),
    waveOutClose: definition('pointer'),
  }).functions;
  const call =
    (name: string) =>
    (...args: (number | bigint | Buffer | null)[]) =>
      Number(f[name](...args));
  native = {
    waveOutOpen: call('waveOutOpen'),
    waveOutPrepareHeader: call('waveOutPrepareHeader'),
    waveOutUnprepareHeader: call('waveOutUnprepareHeader'),
    waveOutWrite: call('waveOutWrite'),
    waveOutReset: call('waveOutReset'),
    waveOutClose: call('waveOutClose'),
    pointer: buffer => ffi.getRawPointer(buffer),
  };
  return native;
}

// An MMRESULT of 0 is success.
function check(result: number, call: string): void {
  if (result) throw new Error(`${call} failed with error ${result}`);
}

// Memory that winmm keeps using after a call returns. An ArrayBuffer's contents live off the JavaScript heap.
const fixed = (size: number) => Buffer.from(new ArrayBuffer(size));

export class WaveOut {
  private readonly handle: bigint;
  private readonly ring: { header: Buffer; data: Buffer; busy: boolean }[] = [];
  private closed = false;

  // Opens the default output for 44.1 kHz 16-bit mono and prepares blocks of blockBytes each.
  constructor(
    private readonly blockBytes: number,
    private readonly api: WinMM = bind(),
  ) {
    // WAVEFORMATEX: format tag 1 (PCM), 1 channel, the sample rate, bytes per second, block align 2, 16 bits and
    // cbSize 0, in 18 bytes. WAVE_MAPPER (0xffffffff) picks the default device and CALLBACK_NULL (0) asks for no
    // callback.
    const format = fixed(18);
    format.writeUInt16LE(1, 0);
    format.writeUInt16LE(1, 2);
    format.writeUInt32LE(sampleRate, 4);
    format.writeUInt32LE(sampleRate * 2, 8);
    format.writeUInt16LE(2, 12);
    format.writeUInt16LE(16, 14);
    const out = fixed(8);
    check(api.waveOutOpen(out, 0xffffffff, format, null, null, 0), 'waveOutOpen');
    this.handle = out.readBigUInt64LE();
    try {
      for (let i = 0; i < ringSize; i++) {
        const header = fixed(headerSize);
        const data = fixed(blockBytes);
        header.writeBigUInt64LE(api.pointer(data), 0);
        header.writeUInt32LE(blockBytes, 8);
        check(api.waveOutPrepareHeader(this.handle, header, headerSize), 'waveOutPrepareHeader');
        this.ring.push({ header, data, busy: false });
      }
    } catch (e) {
      this.close();
      throw e;
    }
  }

  // How many blocks are written and not yet played.
  pending(): number {
    let n = 0;
    for (const block of this.ring) {
      if (block.busy && block.header.readUInt32LE(24) & done) block.busy = false;
      if (block.busy) n++;
    }
    return n;
  }

  // Copies a block into a free slot and queues it, clearing WHDR_DONE from the slot's last use.
  write(block: Buffer): void {
    if (this.closed) throw new Error('the audio output is closed');
    if (block.length > this.blockBytes) throw new Error('audio block too big');
    this.pending();
    const slot = this.ring.find(b => !b.busy);
    if (!slot) throw new Error('no free audio block');
    block.copy(slot.data);
    slot.header.writeUInt32LE(block.length, 8);
    slot.header.writeUInt32LE(slot.header.readUInt32LE(24) & ~done, 24);
    check(this.api.waveOutWrite(this.handle, slot.header, headerSize), 'waveOutWrite');
    slot.busy = true;
  }

  // Reset returns every queued block, so each can be unprepared before the device closes. The ring is dropped only
  // after that, while winmm no longer holds any of it.
  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.api.waveOutReset(this.handle);
    for (const block of this.ring) this.api.waveOutUnprepareHeader(this.handle, block.header, headerSize);
    this.api.waveOutClose(this.handle);
    this.ring.length = 0;
  }
}
