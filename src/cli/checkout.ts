// Gets a Permafrost access key through the browser. The full-screen setup and plain `frost init` both use
// it, and docs/PERMAFROST.md describes the handoff.

import * as config from '../core/config.js';
import {
  startCheckout,
  checkoutURL,
  checkoutLink,
  errCheckoutCancelled,
  errCheckoutTimeout,
} from '../core/checkout.js';
import { Context } from './context.js';

// Saves the key to config.toml straight away, so quitting setup doesn't lose it.
async function saveToken(token: string): Promise<void> {
  const cfg = await config.loadFile().catch(err => {
    if (err !== config.errNoConfig) throw err;
    return config.defaultConfig();
  });
  cfg.storage.permafrost.token = token;
  if (!cfg.storage.backend) cfg.storage.backend = 'permafrost';
  await config.save(cfg);
}

// Starts a checkout and opens it in the browser. `page` is the address to show in case the browser didn't
// open, and `wait` resolves with the key once it comes back.
export async function checkout(
  ctx: Context,
  storage: config.Storage,
  signal = ctx.signal,
): Promise<{ page: string; wait: () => Promise<string> }> {
  // A custom Permafrost server has its own checkout page.
  const pageURL = storage.permafrost.url ? storage.permafrost.url.replace(/\/$/, '') + '/checkout' : checkoutURL;
  const page = storage.permafrost.url ? pageURL.replace(/^https:\/\//, '') : checkoutLink;
  const co = await startCheckout(pageURL);
  await ctx.hooks.openBrowser(co.url).catch(() => {});

  return {
    page,
    wait: async () => {
      let token: string;
      try {
        token = await co.wait(signal);
      } catch (err) {
        if (err === errCheckoutTimeout)
          throw new Error(
            'nothing came back from checkout within 25 minutes, so frost stopped waiting. If you did pay, your access key is on the checkout page and in your Permafrost account',
          );
        if (err === errCheckoutCancelled) throw new Error('checkout was cancelled in the browser');
        throw err;
      } finally {
        await co.close();
      }

      // If saving fails, the error still carries the key so setup can save it at the end.
      try {
        await saveToken(token);
      } catch (err) {
        throw Object.assign(
          new Error(
            "got your access key but couldn't save it yet (" +
              (err as Error).message +
              "). It's saved when you finish setup",
          ),
          { token },
        );
      }
      return token;
    },
  };
}
