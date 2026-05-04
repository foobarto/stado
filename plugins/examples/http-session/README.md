# http-session — reusable HTTP session wrapper

Thin plugin on top of `stado_http_request` that holds a **cookie jar
+ default headers + base URL** across calls. Plugins (or agent loops)
that talk to logged-in REST APIs use this instead of issuing
stateless `stado_http_request` calls.

## What this example demonstrates

1. **Composing on top of v0.30.0's `stado_http_request`.** Plugins
   layer functionality (cookie persistence, header defaulting, URL
   resolution) above the host's generic transport — the host stays
   protocol-thin, the plugin owns the session semantics. Same
   "stado lean core" boundary that webfetch-cached crosses for
   `stado_http_get`.

2. **Persistent state across wasm-instance freshness.** Each tool
   call gets a fresh wasm instance, so plugin globals don't survive.
   Sessions persist to `<workdir>/.cache/stado-http-session/<id>.json`
   instead — same disk-cache pattern as webfetch-cached. Survives
   stado restarts too, which is useful for long-running engagements.

3. **A tool surface that other plugins can import.** Future plugins
   (e.g. `htb-toolkit/netexec`, `htb-toolkit/ldap`) can declare a
   dependency on `http-session-0.1.0` and re-use the cookie jar
   without re-implementing the same RFC 6265 dance.

## Tools

| Tool | Args | Returns |
|------|------|---------|
| `http_session_open` | `{base_url?, default_headers?, session_id?}` | `{session_id, base_url}` |
| `http_session_request` | `{session_id, method, url, headers?, body_b64?, timeout_ms?}` | `{status, headers, body_b64, body_truncated, cookies, session_id}` |
| `http_session_close` | `{session_id}` | `{ok, session_id}` |

`url` may be absolute or relative-to-`base_url`. Cookies harvested
from `Set-Cookie` on each response are re-emitted on subsequent
same-host calls. Default headers from `open()` merge into every
request; per-call headers win on conflict.

## Capabilities

```json
{
  "capabilities": [
    "net:http_request",
    "fs:read:.cache/stado-http-session",
    "fs:write:.cache/stado-http-session"
  ]
}
```

For lab IPs (RFC1918 / loopback / link-local), add
`net:http_request_private` (v0.31.0+). Without it, the host's
strict dial guard refuses private destinations before TLS.

## Build + install

```sh
stado plugin gen-key http-session-demo.seed         # one-time
./build.sh                                          # compile + sign
stado plugin trust <pubkey-hex> "stado example"     # one-time per signer
stado plugin install .
mkdir -p $PWD/.cache/stado-http-session             # cache dir must exist
```

The pubkey is printed by `gen-key` and also written to `author.pubkey`.

## Run from CLI

```sh
# 1. Open a session against a public API:
stado plugin run --with-tool-host --workdir=$PWD \
  http-session-0.1.0 http_session_open \
  '{"base_url":"https://httpbin.org","default_headers":{"User-Agent":"stado-demo"}}'

# Suppose the response was {"session_id":"s-ab12cd","base_url":"https://httpbin.org"}.

# 2. Make a relative-URL request inside the session:
stado plugin run --with-tool-host --workdir=$PWD \
  http-session-0.1.0 http_session_request \
  '{"session_id":"s-ab12cd","method":"GET","url":"/cookies/set?demo=42"}'

# 3. Subsequent same-host requests automatically carry the demo cookie:
stado plugin run --with-tool-host --workdir=$PWD \
  http-session-0.1.0 http_session_request \
  '{"session_id":"s-ab12cd","method":"GET","url":"/cookies"}'
# → response body shows {"cookies": {"demo": "42"}}

# 4. Free the session state:
stado plugin run --with-tool-host --workdir=$PWD \
  http-session-0.1.0 http_session_close '{"session_id":"s-ab12cd"}'
```

## Cookie jar semantics

- Scope: **host only** (no path / domain / secure / HttpOnly /
  expiry enforcement). Adequate for API workflows; not a browser.
- Multi-cookie `Set-Cookie` (host folds into one comma-joined
  header) is split heuristically on the `, <name>=` boundary, so
  attribute commas like `Expires=Mon, 01 Jan 2030 00:00:00 GMT`
  don't fragment a cookie incorrectly.
- Same-name cookie on a later response overwrites the earlier one.

## Why not just use stado_http_request?

You can. But every plugin that does so re-invents:

- `Authorization: Bearer …` injection on every call
- A cookie jar to follow login → authenticated-call flows
- Relative-URL → absolute-URL resolution
- A place to stash `X-CSRF-Token` from one response and re-emit it

This plugin centralizes those four. ~300 lines of plugin code, no
new host surface area.

## See also

- [`docs/features/plugin-authoring.md`](../../../docs/features/plugin-authoring.md)
  — capability table and first-time-author walkthrough
- [`docs/eps/0028-plugin-run-tool-host.md`](../../../docs/eps/0028-plugin-run-tool-host.md)
  — why `--with-tool-host` and `--workdir` are required
- [`plugins/examples/webfetch-cached/`](../webfetch-cached/) — same
  disk-cache-for-state pattern, on top of `stado_http_get`
- [`CHANGELOG.md`](../../../CHANGELOG.md#v0310) — `net:http_request_private`
  for lab IPs
