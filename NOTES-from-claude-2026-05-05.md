# Stado dogfood feedback — 2026-05-05

Built five new example plugins against the current ABI. Found one
genuine bug, three rough-edge suggestions, and a handful of
small ergonomic things. Plugins themselves are in
`plugins/examples/` — see each subdir's README.

## What I built

| Plugin | What it does | Wasm size |
|--------|--------------|-----------|
| `web-search` | DuckDuckGo HTML / SearXNG search, no API key | ~4 MB |
| `image-info` | PNG/JPEG/GIF/WebP/BMP header parser, no decode | ~3 MB |
| `ls`         | Directory listing with structured metadata    | ~3 MB |
| `mcp-client` | MCP streamable-HTTP transport client          | ~3.5 MB |
| `browser`    | Tiny headless "browser": fetch+goquery+cookies | ~6.9 MB |

All five build with `GOOS=wasip1 -buildmode=c-shared`, are signed
with their own demo seeds, and pass smoke tests via
`stado plugin run --with-tool-host`.

The browser plugin has a longer story — see its README for the
"why this isn't a real browser" preamble. Short version: Goja or
QuickJS-in-wasm don't move the needle against modern anti-bot
because the discriminating signals (JA3/JA4, canvas, WebGL) are
either host-only or categorically impossible without rendering.
v0.1 punts on JS execution.

## Bug fix I shipped

**`pluginRunToolHost` doesn't forward `net:http_request_private`.**
A plugin manifest can declare `net:http_request_private`, the
runtime correctly sets `Host.NetHTTPRequestPrivate=true`, the
runtime's `stado_http_request` host import respects it — but
when the plugin invokes `stado_http_request`, the bundled
`httpreq.RequestTool` checks the **`tool.Host`** (i.e. the
`pluginRunToolHost`) for `AllowPrivateNetwork()`, and that
adapter never implemented the method. So loopback / RFC1918
destinations always got rejected from a plugin run, regardless
of manifest.

Patch is in `cmd/stado/plugin_run_tool_host.go` and
`cmd/stado/plugin_run.go` on this branch:

- Add `(pluginRunToolHost).AllowPrivateNetwork() bool`.
- Add a `newPluginRunToolHostWithPolicy(workdir, runner,
  allowPrivateNetwork bool)` constructor; thread the manifest's
  flag through from `plugin_run.go`.

Existing tests still pass; reproduction was running the new
`mcp-client` plugin against a localhost mock MCP server. With
the patch:

```
$ stado plugin run mcp-client-0.1.0 mcp_init \
    '{"endpoint":"http://127.0.0.1:9999/mcp"}' \
    --workdir /tmp/sandbox --with-tool-host
{"session_id":"…","server_info":{"name":"mock","version":"0.0.1"}, …}
```

vs. without:

```
{"error":"initialize: stado_http_request: Post \"http://127.0.0.1:9999/mcp\":
  http_request: private network address 127.0.0.1 for host \"127.0.0.1\" denied"}
```

## Three rough-edge suggestions

### A) `stado_fs_read` should support partial reads

The image-info plugin needs only the first ~64 KiB of any image,
but `stado_fs_read` refuses any file bigger than the buffer cap
the plugin supplies. So image-info has to pass `bufCap = 16 << 20`
(the host's hard cap), allocate a 16 MiB buffer per call, and
read whole images just to look at the header.

Proposed: `stado_fs_read_partial(path, offset, length, buf, cap)`
that returns up to `cap` bytes starting at `offset`. Same cap
gates as `stado_fs_read`. Keeps the existing all-or-nothing
contract for callers that want it; gives header-inspection
plugins a small-buffer path.

### B) `httpreq` could opt into transparent gzip/brotli decompression

The bundled `stado_http_request` returns raw response bytes. Go's
`net/http` transport auto-injects `Accept-Encoding: gzip` and
decompresses transparently — but only if the caller hasn't set
`Accept-Encoding` itself. If a plugin sets
`Accept-Encoding: gzip, deflate, br` (e.g. to match a real Chrome
profile), the transport leaves the body alone and the plugin
gets brotli bytes it can't decode.

The browser plugin works around this by deliberately omitting
`Accept-Encoding` so Go's transport handles gzip behind the
scenes. That's fine, but it's a subtle footgun. Either:

- httpreq detects `Content-Encoding: gzip` / `br` / `deflate` and
  decompresses before returning, OR
- the plugin manifest opts in via `net:http_decompress` and
  httpreq behaves accordingly.

I'd lean toward the first — there's no good reason a plugin
would want raw compressed bytes (and if there is, it can set
`Accept-Encoding: identity`).

### C) Plugin `install` should detect updated wasm with same version

```
$ ./build.sh             # rebuilds plugin.wasm with new sha256
$ stado plugin install .
skipped: image-info v0.1.0 already installed at …
```

The "skipped" path is reasonable if name+version is the identity,
but during plugin authoring it's a constant footgun — you change
a line, rebuild, re-install, run, and you're still running the
old wasm. Workarounds: bump version in the manifest every edit, or
`rm -rf $XDG_DATA_HOME/stado/plugins/<name>-<version>/` before
re-install.

Proposed: when the wasm sha256 in the about-to-install manifest
differs from the installed one, `install` should print
`reinstalling (sha256 changed)` and replace the directory. If the
sha matches, keep the current "skipped" no-op.

Alternatively: `stado plugin install --force` to override the
idempotency check. Simpler than auto-replacing on sha drift.

## Small ergonomic notes

- `stado plugin trust` takes the pubkey on the command line, but
  the build script writes `author.pubkey` (the hex-encoded key)
  alongside the seed. The two could meet — e.g.,
  `stado plugin trust --pubkey-file author.pubkey` (a flag I
  reflexively tried). Workaround `$(cat author.pubkey)` is fine,
  just wanted to note the friction.
- `stado plugin install` with `--with-tool-host` would be a
  natural thing to allow at install time and have it remembered
  in the install dir, so subsequent `stado plugin run` calls
  don't need the flag every time. Right now the operator has to
  remember to add it for any plugin that touches a bundled tool.
- The build dance — `gen-key` → `build.sh` → `trust` → `install` —
  could collapse to `stado plugin dev <dir>` that does all four
  with a development-mode key (TOFU-pinned for local dev only).
  Big quality-of-life for plugin authoring.

## Things I liked

- The plugin ABI is genuinely tiny and the host imports are
  well-named. `stado_log` / `stado_fs_*` / `stado_http_request`
  cover most needs without inventing a new RPC layer.
- `stado plugin doctor <name>-<version>` told me exactly which
  flag I was missing on the first run of the mcp-client plugin
  (`--with-tool-host`). That's the right shape — diagnostic, not
  prescriptive.
- The capability vocabulary maps cleanly to operator intent.
  `fs:read:.cache/stado-mcp` is a self-documenting cap line; I
  could write the manifest before reading any docs.
- The cap-gating warnings (`WARN stado_http_request failed
  plugin=mcp-client err="…private network address … denied"`) are
  actionable. I knew exactly what manifest line to add to fix it.
