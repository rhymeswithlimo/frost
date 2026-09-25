# Permafrost HTTP API

Permafrost is an optional hosted storage service for frost. It's a plain object store with four operations. It never sees plaintext: frost encrypts everything before upload, same as with S3.

This document is the contract. frost's `permafrost` backend (`internal/storage/permafrost`) is a client for it, and its tests run against a reference server built from this document. Anyone can run a compatible server.

Version: `v1`

## Basics

| | |
|---|---|
| Base URL | Whatever you set as `storage.permafrost.url`, e.g. `https://permafrost.example.com` |
| Auth | `Authorization: Bearer <token>` on every request |
| Transport | HTTPS. Clients refuse plain `http://` except for `localhost` and `127.0.0.1` |
| Object bodies | Raw bytes, `Content-Type: application/octet-stream` |
| Other bodies | JSON, UTF-8 |

## Object keys

Keys are paths like `chunks/ab/ab12...` or `snapshots/maple-otter-3f1c`.

- Allowed characters: `a-z`, `0-9`, `.`, `_`, `-`, `/`
- 1 to 1024 bytes, no leading `/`, no empty segments, no `.` or `..` segments
- Case sensitive

Keys go in the URL path as is (the allowed characters need no escaping). Servers must reject anything else with `400 invalid_key`.

## Endpoints

### `PUT /v1/objects/{key}`

Stores the request body under `key`, replacing any existing object.

| Header | Required | Meaning |
|---|---|---|
| `Content-Length` | yes | Body size |
| `X-Content-SHA256` | yes | Lowercase hex SHA-256 of the body. The server rejects the upload with `400 checksum_mismatch` if it doesn't match |

Responses: `204` stored, `400`, `401`, `403`, `413` object too large, `507` account storage full.

Max object size is 16 MiB. frost chunks are at most 8 MiB before encryption.

### `GET /v1/objects/{key}`

Returns the object body.

Responses: `200` with the body and an `X-Content-SHA256` header, `404 not_found`.

Clients should check the body against `X-Content-SHA256`. frost does this, and also authenticates every object with its own key after download.

### `DELETE /v1/objects/{key}`

Deletes the object. Deleting a missing key succeeds.

Responses: `204`.

### `GET /v1/objects?prefix={prefix}&cursor={cursor}`

Lists keys that start with `prefix` (may be empty). Results are paged, up to 1000 keys per page, in any stable order.

```json
{
  "keys": ["chunks/ab/ab12...", "chunks/ab/ab34..."],
  "next_cursor": "opaque-string-or-empty"
}
```

Pass `next_cursor` back as `cursor` to get the next page. An empty or missing `next_cursor` means this was the last page.

Responses: `200`.

## Errors

Every non-2xx response has this body:

```json
{ "error": { "code": "not_found", "message": "object not found" } }
```

| Status | Code | Meaning |
|---|---|---|
| 400 | `invalid_key` | Key breaks the rules above |
| 400 | `checksum_mismatch` | Body doesn't match `X-Content-SHA256` |
| 401 | `unauthorized` | Missing or invalid token |
| 403 | `forbidden` | Token valid but not allowed to do this |
| 404 | `not_found` | No object with that key |
| 413 | `too_large` | Body over the size limit |
| 429 | `rate_limited` | Slow down. See `Retry-After` |
| 500, 502, 503, 504 | `unavailable` | Try again. See `Retry-After` if present |
| 507 | `quota_exceeded` | Account is full |

## Retries

Clients retry `429` and `5xx` responses (except `507`) and network errors, up to 4 attempts in total, with exponential backoff starting at 500 ms. If the server sends `Retry-After` (in seconds), clients wait that long instead. `PUT` and `DELETE` are idempotent, so retrying them is always safe.

## What the server can see

The server sees object keys, sizes, upload times and the account's IP addresses. It can't see file names, file contents, folder structure or snapshot contents, because all of those are inside encrypted objects. Chunk keys are keyed hashes, so the server can't tell whether you have a particular known file. See [SECURITY.md](SECURITY.md).
