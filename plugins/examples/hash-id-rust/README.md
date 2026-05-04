# hash-id-rust — hash identification plugin (Rust SDK proof)

Port of the `hash_identify` tool from the Go `hash` plugin in
`htb-toolkit/hash`, implemented in Rust targeting `wasm32-unknown-unknown`
with `#![no_std]`.

## Purpose

This is the **Rust SDK proof** for stado's polyglot wasm plugin runtime:

| | Go (htb-toolkit/hash) | Rust (this) |
|-|----------------------|-------------|
| wasm size | ~3.5 MB | ~50-200 KB (estimated) |
| Build | `GOOS=wasip1 go build` | `cargo build --target wasm32-unknown-unknown` |
| Std | full Go runtime | `#![no_std]` + bump allocator |
| Dependencies | none beyond stdlib | none |

## Build

**Requires `rustup` + `wasm32-unknown-unknown` target:**

```sh
rustup target add wasm32-unknown-unknown
stado plugin gen-key hash-id-rust-demo.seed   # one-time
./build.sh                                     # cargo + sign
stado plugin trust <pubkey-hex> "stado example"
stado plugin install .
```

The `build.sh` checks for the wasm target and exits with a clear
message if it's missing.

## ABI notes (for Rust plugin authors)

Three differences from Go plugins worth knowing:

1. **`#![no_std]`** — no heap by default. Declare a `#[global_allocator]`
   using a bump allocator over a static `[u8; N]` arena.

2. **Target is `wasm32-unknown-unknown`** (not `wasm32-wasip1`) — the
   stado ABI is freestanding; WASI syscalls are not needed.

3. **Bump arena must be ≥ 2 MiB** — the host calls `stado_alloc` twice
   per tool invocation (args buffer + 1 MiB result buffer), so the arena
   must accommodate both.

```rust
#[no_mangle]
pub extern "C" fn stado_alloc(size: u32) -> u32 { ... }

#[no_mangle]
pub extern "C" fn stado_free(_ptr: u32, _size: u32) {}

#[no_mangle]
pub extern "C" fn stado_tool_hash_identify(
    args_ptr: *const u8, args_len: u32,
    result_ptr: *mut u8, result_cap: u32,
) -> i32 { ... }
```

## See also

- [`plugins/examples/encode-zig/`](../encode-zig/) — Zig SDK proof (4.7 KB)
- [`plugins/examples/hello/`](../hello/) — minimal Zig plugin (800 B)
- [`htb-toolkit/hash/`](../../../../htb-writeups/htb-toolkit/hash/) — full-featured Go version
