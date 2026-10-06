// Worker thread for large crypto jobs queued by crypto-pool.ts. It runs Key.work with copies
// of the subkeys the job needs, then erases the job's input and keys. A `hash` job's input goes
// back to the caller instead, so there's nothing left here to erase.

import { parentPort, isMarkedAsUntransferable } from 'node:worker_threads';
import { Key, type CryptoJob } from './crypto.js';

parentPort!.on('message', (job: CryptoJob) => {
  try {
    const { data: result, id } = Key.work(job);

    // Hand the result's memory back without copying when it can be transferred. Node's shared
    // Buffer pool can't be, so small pooled results are copied.
    const data =
      result.buffer instanceof ArrayBuffer && !isMarkedAsUntransferable(result.buffer)
        ? new Uint8Array(result.buffer, result.byteOffset, result.byteLength)
        : new Uint8Array(result);
    parentPort!.postMessage({ data, id }, [data.buffer as ArrayBuffer]);
  } catch (error) {
    // Only the message crosses back, so Repo.putChunk recognises a chunk ID mismatch by its text.
    parentPort!.postMessage({ error: error instanceof Error ? error.message : 'crypto worker failed' });
  } finally {
    // A buffer handed back is detached here, with length 0, and filling a detached array throws.
    if (job.data.byteLength) job.data.fill(0);
    job.enc.fill(0);
    job.mac.fill(0);
  }
});
