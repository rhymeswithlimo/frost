// Permafrost checkout from the terminal. frost opens the checkout page with a loopback redirect,
// then a one-shot local server waits for the page to send the access key back.

import { createServer, type Server, type IncomingMessage, type ServerResponse } from 'node:http';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { isLoopback, readBounded } from './storage.js';

export const checkoutURL = 'https://frost.example.com/checkout.html';
export const checkoutLink = 'getfro.st/perma';
const checkoutTimeout = 25 * 60e3;
export const errCheckoutTimeout = new Error('checkout timed out');
export const errCheckoutCancelled = new Error('checkout cancelled');

interface CheckoutResult {
  token?: string;
  error?: Error;
}

// The page the browser shows after the redirect, in the TUI's colours. Text is HTML-escaped.
function donePage(response: ServerResponse, status: number, title: string, message: string): void {
  const escape = (text: string) =>
    text.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&#34;', "'": '&#39;' })[c]!);
  response.writeHead(status, {
    'Content-Type': 'text/html; charset=utf-8',
    'Cache-Control': 'no-store',
    'Referrer-Policy': 'no-referrer',
  });
  response.end(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>frost</title>
<body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#1926c4;color:#f2efe7;font:16px/1.5 ui-monospace,Menlo,Consolas,monospace">
<main style="max-width:32rem;padding:2rem;border:1px solid #f2efe7"><p style="margin:0 0 1rem;font-weight:bold">${escape(title)}</p><p style="margin:0;color:#b1aea9">${escape(message)}</p></main>`);
}

// One pending checkout. `state` is a random secret the redirect must echo, so a request that
// didn't come from this checkout can't hand frost a key.
export class Checkout {
  #result: Promise<CheckoutResult>;
  #finish!: (value: CheckoutResult) => void;
  #finished = false;
  #closing?: Promise<void>;

  constructor(
    public url: string,
    private server: Server,
    private state: string,
  ) {
    this.#result = new Promise(resolve => {
      this.#finish = resolve;
    });
  }

  // Handles the redirect. The page can send its fields in the query string or as a form POST
  // of at most 16 KiB. Only the first callback with the right state decides the result.
  async callback(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const incoming = new URL(request.url ?? '/', 'http://127.0.0.1');
    const fail = (status: number, text: string) => {
      response.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8', 'X-Content-Type-Options': 'nosniff' });
      response.end(text + '\n');
    };
    if (incoming.pathname !== '/callback') {
      fail(404, '404 page not found');
      return;
    }
    if (request.method !== 'GET' && request.method !== 'POST') {
      fail(405, 'method not allowed');
      return;
    }

    // Form fields override query fields with the same name.
    const values = incoming.searchParams;
    if (request.method === 'POST') {
      try {
        const content = await readBounded(request, 16 << 10, Number(request.headers['content-length'] ?? -1));
        if (request.headers['content-type']?.split(';')[0] === 'application/x-www-form-urlencoded') {
          const form = new URLSearchParams(content.toString());
          for (const [name, value] of form) values.set(name, value);
        }
      } catch {
        fail(400, 'invalid callback');
        return;
      }
    }

    // Compare the state in constant time.
    const state = values.get('state') ?? '';
    if (!state) {
      fail(400, 'missing state');
      return;
    }
    const received = Buffer.from(state);
    const expected = Buffer.from(this.state);
    if (received.length !== expected.length || !timingSafeEqual(received, expected)) {
      donePage(
        response,
        400,
        "This doesn't match the checkout frost started.",
        'frost is still waiting for the original checkout.',
      );
      return;
    }

    const token = (values.get('token') ?? '').trim();
    let result: CheckoutResult;
    if (values.get('error')) {
      result = { error: errCheckoutCancelled };
      donePage(response, 200, 'Checkout cancelled.', 'Go back to your terminal to try again or paste a key.');
    } else if (!token || /[ \t\r\n]/.test(token)) {
      result = { error: new Error("the checkout page didn't send back an access key") };
      donePage(
        response,
        400,
        'No access key came back.',
        'Go back to your terminal and paste your key from your Permafrost account.',
      );
    } else {
      result = { token };
      donePage(
        response,
        200,
        "You're all set.",
        'Your access key is with frost. You can close this tab and go back to your terminal.',
      );
    }
    if (!this.#finished) {
      this.#finished = true;
      this.#finish(result);
    }
  }

  // Waits for the callback, the timeout or cancellation, then always shuts the server down.
  async wait(signal?: AbortSignal, timeout = checkoutTimeout): Promise<string> {
    let timer: NodeJS.Timeout | undefined;
    let abort: (() => void) | undefined;
    try {
      signal?.throwIfAborted();
      const stopped = new Promise<CheckoutResult>((_, reject) => {
        timer = setTimeout(() => reject(errCheckoutTimeout), timeout);
        abort = () => reject(signal!.reason);
        signal?.addEventListener('abort', abort, { once: true });
      });
      const result = await Promise.race([this.#result, stopped]);
      if (result.error) throw result.error;
      return result.token!;
    } finally {
      if (timer) clearTimeout(timer);
      if (abort) signal?.removeEventListener('abort', abort);
      await this.close();
    }
  }

  // Stops listening. Open connections get two seconds to finish before they're cut.
  close(): Promise<void> {
    if (!this.#closing)
      this.#closing = new Promise(resolve => {
        const timer = setTimeout(() => {
          this.server.closeAllConnections();
        }, 2000);
        this.server.close(() => {
          clearTimeout(timer);
          resolve();
        });
      });
    return this.#closing;
  }
}

// Starts a loopback server on a random port and adds its redirect address and the state to the
// checkout URL. The page must use https, or http on a loopback host for local testing.
export async function startCheckout(pageURL: string): Promise<Checkout> {
  let page: URL;
  try {
    page = new URL(pageURL);
  } catch {
    throw new Error(`permafrost: invalid checkout url ${JSON.stringify(pageURL)}`);
  }
  if (
    !page.host ||
    page.username ||
    page.password ||
    (page.protocol !== 'https:' && !(page.protocol === 'http:' && isLoopback(page.hostname)))
  )
    throw new Error(`permafrost: invalid checkout url ${JSON.stringify(pageURL)}`);

  const state = randomBytes(32).toString('base64url');
  let checkout!: Checkout;
  const server = createServer((request, response) => {
    void checkout.callback(request, response).catch(() => {
      if (!response.headersSent) response.writeHead(400);
      response.end('invalid callback\n');
    });
  });

  // Short timeouts stop a stalled connection from holding the server.
  server.headersTimeout = 10e3;
  server.requestTimeout = 10e3;
  server.setTimeout(10e3);
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', reject);
      resolve();
    });
  });

  const address = server.address();
  if (!address || typeof address === 'string') throw new Error("permafrost: can't listen for checkout");
  page.searchParams.set('redirect_uri', `http://127.0.0.1:${address.port}/callback`);
  page.searchParams.set('state', state);
  checkout = new Checkout(page.toString(), server, state);
  return checkout;
}
