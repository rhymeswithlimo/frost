// A small pool of worker threads that seal and open large chunks off the main thread.
// Jobs transfer their buffers to a worker, which erases them when it's done.

import { Worker } from 'node:worker_threads';
import { availableParallelism } from 'node:os';
import type { CryptoJob } from './crypto.js';

// A worker's output, and the chunk ID a `hash` job computed.
interface CryptoReply {
  data: Buffer;
  id?: string;
}

interface Pending {
  job: CryptoJob;
  resolve: (reply: CryptoReply) => void;
  reject: (error: Error) => void;
}

interface Slot {
  worker: Worker;
  pending?: Pending;
  // Set once the worker has started and can take a job without a startup delay.
  ready?: boolean;
}

const slots: Slot[] = [];
const waiting: Pending[] = [];

// At most four workers, leaving a core for the main thread.
const count = Math.min(4, Math.max(1, availableParallelism() - 1));

// Settles the slot's job with the worker's reply, then lets the slot take the next waiting job.
function finish(slot: Slot, result?: { data?: Uint8Array; id?: string; error?: string }): void {
  const pending = slot.pending;
  slot.pending = undefined;
  if (pending) {
    if (result?.data)
      pending.resolve({
        data: Buffer.from(result.data.buffer, result.data.byteOffset, result.data.byteLength),
        id: result.id,
      });
    else pending.reject(new Error(result?.error ?? 'crypto worker stopped'));
  }

  // Idle workers don't keep the process alive.
  slot.worker.unref();
  pump();
}

// Starts a worker. A worker that fails or exits leaves the pool and rejects its current job.
function start(): Slot {
  const worker = new Worker(new URL('./crypto-worker.js', import.meta.url), { execArgv: [] });
  const slot: Slot = { worker };
  slots.push(slot);
  worker.unref();
  worker.once('online', () => (slot.ready = true));

  worker.on('message', (result: { data?: Uint8Array; id?: string; error?: string }) => finish(slot, result));

  worker.on('error', error => {
    const i = slots.indexOf(slot);
    if (i >= 0) slots.splice(i, 1);
    const pending = slot.pending;
    slot.pending = undefined;
    pending?.reject(error);
    pump();
  });

  worker.on('exit', code => {
    const i = slots.indexOf(slot);
    if (i >= 0) slots.splice(i, 1);
    if (slot.pending) {
      const pending = slot.pending;
      slot.pending = undefined;
      pending.reject(new Error(`crypto worker exited (${code})`));
    }
    pump();
  });

  return slot;
}

// Gives waiting jobs to idle workers, starting new workers up to the limit.
function pump(): void {
  while (waiting.length) {
    let slot = slots.find(slot => !slot.pending);
    if (!slot) {
      if (slots.length >= count) return;
      slot = start();
    }

    const pending = waiting.shift()!;
    slot.pending = pending;
    slot.worker.ref();

    // The input and both subkeys move to the worker. If posting fails, erase them here instead.
    try {
      slot.worker.postMessage(pending.job, [
        pending.job.data.buffer as ArrayBuffer,
        pending.job.enc.buffer as ArrayBuffer,
        pending.job.mac.buffer as ArrayBuffer,
      ]);
    } catch (error) {
      pending.job.data.fill(0);
      pending.job.enc.fill(0);
      pending.job.mac.fill(0);
      slot.pending = undefined;
      pending.reject(error as Error);
      slot.worker.unref();
    }
  }
}

// Queues a job and resolves with the worker's output. The job's buffers are transferred, so
// callers must pass buffers they own and won't read again.
// Whether a worker has finished starting. Until one has, a job would wait for a new thread.
export function workerReady(): boolean {
  return slots.some(slot => slot.ready);
}

// A `hash` job holds up the caller's next step, so it goes ahead of other waiting jobs, behind
// any hash jobs already waiting.
export function runCryptoWorker(job: CryptoJob): Promise<CryptoReply> {
  return new Promise((resolve, reject) => {
    const pending = { job, resolve, reject };
    if (job.kind === 'hash') {
      const first = waiting.findIndex(other => other.job.kind !== 'hash');
      waiting.splice(first < 0 ? waiting.length : first, 0, pending);
    } else waiting.push(pending);
    pump();
  });
}
