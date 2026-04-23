# hello-go — example WASM plugin for stado (Go edition)

Same contract as [`../hello/`](../hello/) (the Zig example), implemented
in Go. Use it as a reference when authoring plugins in Go — it shows the
`//go:wasmexport` / `//go:wasmimport` directives, safe pointer/slice
access through `unsafe.Slice`, and how to keep host-allocated buffers
alive across the stado_alloc/stado_free lifecycle.

Size comparison: the Go build is **~1.5–3 MB** (includes the full Go
runtime — scheduler, GC, allocator). The Zig sibling is **~800 bytes**.
Pick Go when you want stdlib + existing packages; pick Zig / Rust /
TinyGo when wasm size matters.

## Layout

```
plugins/examples/hello-go/
├── README.md                      — this file
├── build.sh                       — compile + sign helper
├── go.mod                         — standalone module so stado's go.sum
│                                    stays out of the wasip1 build
├── main.go                        — source
└── plugin.manifest.template.json  — committed template
```

`plugin.wasm`, `plugin.manifest.json`, `plugin.manifest.sig`, and
`hello-go-demo.seed` are generated and gitignored.

## Prerequisites

- **Go 1.24+** on `$PATH` (1.25+ recommended — `-buildmode=c-shared`
  for `GOOS=wasip1` landed in 1.24; earlier versions can't produce a
  reactor module). Plain `go build` in command-style mode won't work
  — the resulting wasm calls `proc_exit` on start and the host can't
  invoke exports.
- **stado** on `$PATH` (or `STADO=/path/to/stado`).

## End-to-end walkthrough

From this directory:

```sh
# 1. One-time: generate the demo signer's Ed25519 keypair.
stado plugin gen-key hello-go-demo.seed

# 2. Compile + sign.
./build.sh

# 3. Pin the demo pubkey on verifier machines.
stado plugin trust <pubkey-hex> "stado example go"

# 4. Verify + install.
stado plugin verify .
stado plugin install .

# 5. Run.
stado plugin run hello-go-0.1.0 greet '{"name":"Ada"}'
# → {"message":"Hello, Ada!"}
```

## How the Go source wires the ABI

```go
//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { ... }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { ... }

//go:wasmexport stado_tool_greet
func stadoToolGreet(argsPtr, argsLen, resultPtr, resultCap int32) int32 { ... }
```

Three non-obvious details:

1. **`-buildmode=c-shared` + reactor mode.** stado's runtime knows to
   call `_initialize` after instantiation (alongside `_start`), so Go
   plugins compiled this way boot their runtime before any
   `//go:wasmexport` is invoked. Command-mode Go wasm can't be used
   as a plugin — its `_start` runs `main` and `proc_exit`s.

2. **Pin allocations.** `make([]byte, size)` returns a slice whose
   backing store lives in wasm linear memory, but Go's GC is free to
   reclaim it after the export returns. The plugin puts the backing
   store in a `sync.Map` keyed by address so it survives until the
   paired `stado_free` arrives.

3. **Pointer casts.** `unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), n)`
   produces a zero-copy view of host memory at the given offset.
   wasm32 pointers fit in `int32`, so no width conversion is needed.

## What the plugin demonstrates

- The same `greet` tool implementation as the Zig sibling — pick up
  this file to see the Go idioms side-by-side with a freestanding
  non-Go target.
- `encoding/json` for args/result marshalling (happens entirely inside
  the wasm module; no cgo).
- `stado_log` host import — you'll see `INFO greet invoked
  plugin=hello-go` in stderr when the tool runs.
- `"capabilities": []` — filesystem/net host imports are denied by the
  sandbox. This plugin doesn't need them; it only reads args and
  writes a result buffer through the provided pointers.

## Iterating

Edit `main.go`, `./build.sh`, repeat. Bump `version` in
`plugin.manifest.template.json` (or delete the old install directory)
before reinstalling over an existing version — rollback protection
refuses downgrades, and install rejects same-version overwrites.
