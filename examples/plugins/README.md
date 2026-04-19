# Example WASM plugins for stado

Each subdirectory is a self-contained, signable plugin. The ABI is the
same across all of them; the source language isn't. Pick whichever
matches the toolchain you already have.

| Example                                       | Language | Wasm size | Notes                                                                                                     |
|-----------------------------------------------|----------|-----------|-----------------------------------------------------------------------------------------------------------|
| [`hello/`](hello/)                             | Zig      | ~800 B    | freestanding wasm32, no runtime                                                                           |
| [`hello-go/`](hello-go/)                       | Go       | ~3 MB     | reactor via `-buildmode=c-shared`, WASIp1                                                                  |
| [`session-inspect/`](session-inspect/)         | Go       | ~3 MB     | Phase 7.1b capability demo — declares `session:read` / `session:fork` / `llm:invoke`, exercises the first |

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
