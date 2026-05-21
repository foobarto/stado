# Optional WASM plugins for stado

Opt-in plugins. Operator installs each one explicitly via
`stado plugin install`. Distinct from
[`../bundled/`](../bundled/), which ships compiled into the stado
binary and is available in every session without operator action,
and from [`../demos/`](../demos/), which holds plugin-author
showcases and approval-gate test fixtures rather than user-facing
tools — see [`../README.md`](../README.md) for the full split.

Each subdirectory is a self-contained, signable plugin. The ABI is the
same across all of them; the source language isn't. Pick whichever
matches the toolchain you already have.

| Plugin | Lang | Wasm size | Role |
|---|---|---|---|
| [`browser/`](browser/) | Go | ~6.9 MB | Two-tier browser: tier-1 HTTP + goquery + cookie jar, tier-2 launches headless Chrome via `stado_proc_spawn` + CDP. Recommended browser for agent workflows. |
| [`browser-minimal/`](browser-minimal/) | Go | ~3.5 MB | Tier-1-only browser pattern: fetch + parse + cookie jar + form submit + UA spoofing, no Chrome. Copy-modify reference; not a real browser. |
| [`encode-zig/`](encode-zig/) | Zig | ~8 KB | Encode/decode between common formats (base64, hex, url, …). Pure-compute Zig, freestanding wasm32. |
| [`hash-id-rust/`](hash-id-rust/) | Rust | ~built per-plugin | Identify hash types from a sample. Returns candidate list with hashcat mode + john format. |
| [`http-session/`](http-session/) | Go | ~3.6 MB | Reusable HTTP session: returns a session_id; subsequent calls reuse cookies/headers/auth across requests. |
| [`image-info/`](image-info/) | Go | ~3.2 MB | Header-only image metadata: PNG, JPEG, GIF87a/89a, WebP, BMP. Parses magic + dimensions, never decodes pixels. |
| [`mcp-client/`](mcp-client/) | Go | ~3.5 MB | MCP streamable-HTTP transport client. `mcp_init` / `mcp_list_tools` / `mcp_call_tool`. Persists session under `<workdir>/.cache/stado-mcp/`. |
| [`persistent-shell/`](persistent-shell/) | Go | ~3.3 MB | PTY-backed shell session with persistent id; output buffered for later read. |
| [`session-inspect/`](session-inspect/) | Go | ~3 MB | Read current session shape — id, message count, token count, last turn ref. Single-cap (`session:read`) demo. |
| [`session-recorder/`](session-recorder/) | Go | ~3 MB | Background plugin: appends one JSONL row per turn to `.stado/session-recordings.jsonl`. Capability mix `session:read` + `fs:write` + `stado_plugin_tick` — the second background plugin after auto-compact. |
| [`web-search/`](web-search/) | Go | ~4 MB | DuckDuckGo HTML / SearXNG search wrapper. No API key. Returns `{title, url, snippet}` triples. |
| [`webfetch-cached/`](webfetch-cached/) | Go | ~3.4 MB | Wraps `stado_http_get` behind a SHA-256-keyed disk cache. Showcases wrapping a bundled-tool host import, workdir-rooted fs caps, and bundled-tool overrides. |

## Bigger picture

The stado plugin ABI is intentionally small:

```
exports:
  stado_alloc(size) → ptr
  stado_free(ptr, size)
  stado_tool_<name>(argsPtr, argsLen, resultPtr, resultCap) → n_or_-1

imports (from module "stado"):
  stado_log(levelPtr, levelLen, msgPtr, msgLen)
  stado_fs_read(pathPtr, pathLen, bufPtr, bufCap) → n_or_-1    // cap-gated
  stado_fs_write(pathPtr, pathLen, bufPtr, bufLen) → n_or_-1   // cap-gated
```

Any wasm toolchain that can hit those exports + the freestanding ABI
will work. The runtime tries `_start` then `_initialize` so both
command-style (Zig/Rust/TinyGo freestanding) and reactor-style (Go
`c-shared`) modules boot correctly.

## See also

- [`PLAN.md` §7](../../PLAN.md) — plugin manifest, signing, trust
  store, CRL, Rekor — the full security model around what you're
  about to load.
- [`internal/plugins/runtime/`](../../internal/plugins/runtime/) —
  host implementation. `host.go` is the authoritative spec for what
  the host imports do.
