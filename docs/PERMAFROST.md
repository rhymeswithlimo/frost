# Permafrost

Permafrost is a hosted storage service for frost. It's a plain object store with four operations. Backup objects are encrypted before upload, the same as with S3. Setup also writes and deletes a small plaintext connectivity probe.

This document is the contract. `PermafrostBackend` in `src/core/storage.ts` implements it. Its tests in `test/core/storage.test.ts` run against a loopback reference server built from this document. Anyone can run a compatible server.

Version: `v1`

## Basics

| | |
|---|---|
| Base URL | `storage.permafrost.url`, or the default server when that's blank |
| Auth | `Authorization: Bearer <token>` on every request |
| Transport | HTTPS. Clients refuse plain `http://` except for loopback hosts like `localhost` and `127.0.0.1` |
| Object bodies | Raw bytes, `Content-Type: application/octet-stream` |
| Other bodies | JSON, UTF-8 |

## Object keys

Keys are paths like `chunks/ab/ab12...` or `snapshots/maple-absurd-3f1c9a0b2e7`.

- Allowed characters are `a-z`, `0-9`, `.`, `_`, `-` and `/`.
- A key is 1 to 1024 bytes, with no leading `/`, no empty segments, and no `.` or `..` segments.
- Keys are case sensitive.

Keys go in the URL path as they are, since the allowed characters need no escaping. Servers must reject anything else with `400 invalid_key`.

## Endpoints

### `PUT /v1/objects/{key}`

Stores the request body under `key`, replacing any existing object.

| Header | Required | Meaning |
|---|---|---|
| `Content-Length` | yes | Body size |
| `X-Content-SHA256` | yes | Lowercase hex SHA-256 of the body. The server rejects the upload with `400 checksum_mismatch` if it doesn't match |
| `If-None-Match` | no | When `*`, create the object atomically only if the key is absent. Otherwise return `412 already_exists` without changing the existing object |

Responses: `204` stored, `400`, `401`, `403`, `412 already_exists`, `413` object too large, `507` account storage full.

frost uses conditional writes for `frost.repo`, snapshot headers and trees, so they're required. Servers must enforce them atomically, including for requests from different clients. A conditional request must never overwrite an existing object.

The maximum object size is 16 MiB. frost chunks, including the ones holding snapshot file lists, are at most 8 MiB before encryption, and everything else it stores is small.

### `GET /v1/objects/{key}`

Returns the object body.

Responses: `200` with the body and an `X-Content-SHA256` header, `404 not_found`.

Clients should check the body against `X-Content-SHA256`. frost does, and it also authenticates every object with its own key after download.

### `DELETE /v1/objects/{key}`

Deletes the object. Deleting a missing key succeeds.

Responses: `204`.

### `GET /v1/objects?prefix={prefix}&cursor={cursor}`

Lists the keys that start with `prefix`, which may be empty. Results come in pages of up to 1000 keys, in any stable order.

```json
{
  "keys": ["chunks/ab/ab12...", "chunks/ab/ab34..."],
  "next_cursor": "opaque-string-or-empty"
}
```

Pass `next_cursor` back as `cursor` to get the next page. An empty or missing `next_cursor` means this was the last page.

Cursor values must not repeat within a listing. frost rejects cursor cycles.

Responses: `200`.

## Getting a key

`frost init` can get an access key in the browser, the same way desktop apps sign in.

1. frost listens on `127.0.0.1` on a free port and opens the checkout page with two parameters added:

   | Parameter | Meaning |
   |---|---|
   | `redirect_uri` | `http://127.0.0.1:{port}/callback` |
   | `state` | A random value, 32 bytes, base64url |

   For the default server, the page is on the frost website. For a custom server, it's `{base}/checkout`. It's a web page for a person, not part of the API, so it isn't under `/v1` and doesn't take a token.

2. Once the person has a key, the page sends the browser to `redirect_uri` with `state` and `token`, as a query string (`GET`) or a form (`POST`, `application/x-www-form-urlencoded`). A POST keeps the key out of the browser's history. If they cancel, the page sends `state` and `error=cancelled` instead.

3. frost ignores requests to other paths and callbacks without a `state`. A callback with the wrong `state` gets an error page, and frost keeps waiting for the right one. Once the right `state` arrives, frost saves `token` as the access key and stops listening. It waits 25 minutes at most.

The page must:

- Only send the key to a `redirect_uri` on `127.0.0.1` or `localhost`. Anything else would let a crafted link send someone's key to another site.
- Show the key once it's issued, with or without a `redirect_uri`. The redirect can't reach frost when the browser is on another machine or frost has stopped waiting, and people who come straight from the website have no frost waiting at all.

Because frost checks that the redirect carries the `state` it generated, another page can't feed it a key of its own. Callback bodies are limited to 16 KiB, and only this machine can receive the redirect.

## Errors

Every non-2xx response has this body:

```json
{ "error": { "code": "not_found", "message": "object not found" } }
```

| Status | Code | Meaning |
|---|---|---|
| 400 | `invalid_key` | The key breaks the rules above |
| 400 | `checksum_mismatch` | The body doesn't match `X-Content-SHA256` |
| 401 | `unauthorized` | Missing or invalid token |
| 403 | `forbidden` | The token is valid but not allowed to do this |
| 404 | `not_found` | No object with that key |
| 413 | `too_large` | The body is over the size limit |
| 429 | `rate_limited` | Slow down, and see `Retry-After` |
| 500, 502, 503, 504 | `unavailable` | Try again, after `Retry-After` if it's set |
| 507 | `quota_exceeded` | The account is full |

## Retries

Clients retry `429` and `5xx` responses (except `507`) and network errors, up to 4 attempts in total, with exponential backoff starting at 500 ms. `Retry-After` is read as seconds, capped at 60. Retrying a conditional PUT after a lost response can return `412` even if the first request succeeded. The client then reports a failure rather than overwrite the object.

Clients don't follow redirects from the API, so configure the final HTTPS endpoint directly. Base URLs must not contain credentials, a query or a fragment.

## What the server can see

The server sees object keys, sizes, upload times and the account's IP addresses. It can't see file names, file contents, folder structure or snapshot contents, because all of those are inside encrypted objects. Chunk keys are keyed hashes, so the server can't tell whether you have a particular known file. [SECURITY.md](SECURITY.md) has the full picture.
