# encode-zig — encoding/decoding plugin (Zig SDK proof)

Single tool `encode` covering base64, base64url, hex, URL percent-encoding,
and HTML entity escaping — with decode support for all except HTML.

## Purpose

This is the **Zig SDK proof** for stado's polyglot wasm plugin runtime.
The same functionality implemented in Go would be ~3.5 MB; this plugin
is **~5 KB** — a 700× size reduction. Useful for:

- Demonstrating to plugin authors that non-Go toolchains work with the
  stado ABI (the exports `stado_alloc`, `stado_free`, `stado_tool_*`
  and the imports `stado_log`, `stado_fs_read` etc. are language-agnostic).
- Providing a readable reference for Zig authors learning the ABI (the
  `hello` example is minimal; this one adds a realistic tool with
  multiple code paths).
- Actual encoding utility during engagement work.

## What it demonstrates

- **`wasm32-freestanding`** target (no WASI, no libc, no std.debug).
- **Bump allocator** over a fixed 2 MiB arena — the host calls
  `stado_alloc` twice per tool call (once for args, once for result)
  so the arena must be at least 2× the result-cap (1 MiB default).
- **Manual JSON parsing** (`jsonStr`) and **JSON output construction**
  without any allocator or third-party crate.
- All logic in a single 250-line `.zig` file with no build system.

## Build

```sh
zig --version               # requires >= 0.13.0
stado plugin gen-key encode-zig-demo.seed   # one-time
./build.sh                                  # compile + sign
stado plugin trust <pubkey-hex> "stado example"
stado plugin install .
```

## Run

```sh
# Encode
stado plugin run --with-tool-host encode-zig-0.1.0 encode \
  '{"data":"Hello, world!","format":"base64"}'
# → {"result":"SGVsbG8sIHdvcmxkIQ==","format":"base64","direction":"encode"}

# Decode
stado plugin run --with-tool-host encode-zig-0.1.0 encode \
  '{"data":"SGVsbG8sIHdvcmxkIQ==","format":"base64","direction":"decode"}'
# → {"result":"Hello, world!","format":"base64","direction":"decode"}

# HTML escaping
stado plugin run --with-tool-host encode-zig-0.1.0 encode \
  '{"data":"<script>alert(1)</script>","format":"html"}'

# URL percent-encoding
stado plugin run --with-tool-host encode-zig-0.1.0 encode \
  '{"data":"hello world/path?q=1","format":"url"}'

# Hex
stado plugin run --with-tool-host encode-zig-0.1.0 encode \
  '{"data":"deadbeef","format":"hex","direction":"decode"}'
```

## Formats

| Format | Encode | Decode |
|--------|--------|--------|
| `base64` | RFC 4648 standard with `=` padding | ✓ |
| `base64url` | URL-safe alphabet (`-_`), with `=` padding | ✓ |
| `hex` | lowercase hex pairs | ✓ |
| `url` | RFC 3986 percent-encoding, unreserved chars pass through | ✓ |
| `html` | `&amp;` `&lt;` `&gt;` `&quot;` `&#39;` | encode-only |

## See also

- [`plugins/examples/hello/`](../hello/) — minimal Zig plugin (800 bytes)
- [`plugins/examples/http-session/`](../http-session/) — Go plugin (~3.5 MB) for comparison
- [`docs/features/plugin-authoring.md`](../../../docs/features/plugin-authoring.md) — capability table
