# Example WASM plugins for stado

These are opt-in sample plugins. They are not enabled by default.
The shipped bundled auto-compaction plugin lives separately at
[`../default/auto-compact/`](../default/auto-compact/).

Each subdirectory is a self-contained, signable plugin. The ABI is the
same across all of them; the source language isn't. Pick whichever
matches the toolchain you already have.

| Example                                       | Language | Wasm size | Notes                                                                                                     |
|-----------------------------------------------|----------|-----------|-----------------------------------------------------------------------------------------------------------|
| [`hello/`](hello/)                             | Zig      | ~800 B    | freestanding wasm32, no runtime                                                                           |
| [`hello-go/`](hello-go/)                       | Go       | ~3 MB     | reactor via `-buildmode=c-shared`, WASIp1                                                                  |
| [`approval-bash-go/`](approval-bash-go/)       | Go       | ~3 MB     | Non-default override for `bash`. Uses `ui:approval` before delegating to the standard host wrapper |
| [`approval-write-go/`](approval-write-go/)     | Go       | ~3 MB     | Non-default override for `write`. Uses `ui:approval` before delegating to the standard host wrapper |
| [`approval-edit-go/`](approval-edit-go/)       | Go       | ~3 MB     | Non-default override for `edit`. Uses `ui:approval` before delegating to the standard host wrapper |
| [`approval-ast-grep-go/`](approval-ast-grep-go/) | Go     | ~3 MB     | Non-default override for `ast_grep`. Search-only runs directly; rewrite mode asks for approval first |
| [`session-inspect/`](session-inspect/)         | Go       | ~3 MB     | Phase 7.1b capability demo — declares `session:read` / `session:fork` / `llm:invoke`, exercises the first |
| [`session-recorder/`](session-recorder/)       | Go       | ~3 MB     | Phase 7.1b second validator — `session:read` + `fs:read`/`fs:write` + `stado_plugin_tick`. Appends a JSONL line per turn to `.stado/session-recordings.jsonl`. Different capability mix from auto-compact, same ABI — proves the surface is general-purpose |
| [`webfetch-cached/`](webfetch-cached/)         | Go       | ~3.5 MB   | v0.26.0 surface demo — wraps the bundled `stado_http_get` host import behind a SHA-256-keyed disk cache. Showcases `--with-tool-host`, workdir-rooted fs caps, and `[tools].overrides` for transparent bundled-tool replacement |
| [`state-dir-info/`](state-dir-info/)           | Go       | ~3 MB     | EP-0029 `cfg:state_dir` capability example — minimal plugin that calls `stado_cfg_state_dir` and returns the resolved path. Copy as a starting template for plugins that need to compose paths under stado's state directory |
| [`web-search/`](web-search/)                   | Go       | ~4 MB     | DuckDuckGo HTML / SearXNG search wrapper. No API key. Returns parsed `{title, url, snippet}` triples. Single-cap (`net:http_request`) example of the bundled `stado_http_request` host import |
| [`image-info/`](image-info/)                   | Go       | ~3 MB     | Header-only image metadata: PNG, JPEG, GIF87a/89a, WebP (VP8 / VP8L / VP8X), BMP. Reads the file via `stado_fs_read`, parses the magic + dimensions, never decodes pixels |
| [`ls/`](ls/)                                   | Go       | ~3 MB     | Single-directory listing with structured metadata (name, type, size, mode, mtime). Wraps `ls -la --time-style=long-iso` under `stado_exec_bash`. Demonstrates the `exec:bash` cap path |
| [`mcp-client/`](mcp-client/)                   | Go       | ~3.5 MB   | MCP (Model Context Protocol) streamable-HTTP transport client. `mcp_init` / `mcp_list_tools` / `mcp_call_tool`. Persists session id + auth headers across invocations under `<workdir>/.cache/stado-mcp/` |
| [`browser/`](browser/)                         | Go       | ~6.9 MB   | Tiny headless "browser": fetch + parse + cookie jar + form submit + UA spoofing. NOT a real browser — no JS, no rendering, no canvas/WebGL. Wraps `stado_http_request` + goquery + `golang.org/x/net/html`. See its README for the "what it can't do" preamble |

Both implement the same tool contract so you can diff them:

```json
// input
{"name": "Ada"}

// output
{"message": "Hello, Ada!"}
```

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
