# hello — example WASM plugin for stado

A minimal, end-to-end example plugin. It exports a single tool, `greet`,
that takes `{"name":"..."}` and returns `{"message":"Hello, <name>!"}`.
The source is ~120 lines of Zig; the compiled module is ~800 bytes.

Layout:

```
plugins/examples/hello/
├── README.md                       — this file
├── build.sh                        — compile + sign helper
├── plugin.manifest.template.json   — committed, empty digest/fpr
├── plugin.manifest.json            — written by build.sh         (gitignored*)
├── plugin.manifest.sig             — written by `stado plugin sign` (gitignored*)
├── plugin.wasm                     — written by zig build-exe     (gitignored*)
├── hello-demo.seed                 — Ed25519 seed for the demo    (gitignored*)
└── src/
    └── hello.zig                   — source
```

\*All generated artefacts stay in `.gitignore`. Only source + template
live in git; run `./build.sh` once to regenerate everything else.

## What the plugin demonstrates

- **Stado's plugin ABI**: `stado_alloc`, `stado_free`, and
  `stado_tool_<name>` exports.
- **A host import**: `stado_log` — the plugin writes an info-level log
  line when the tool is invoked. You'll see it in stado's stderr:
  `INFO greet invoked plugin=hello`.
- **Input parsing** without pulling in a JSON library: a tiny
  key-value extractor over the raw JSON bytes. Real plugins using
  serde/zig-json/etc. would just deserialise normally.
- **Zero capabilities**: the manifest declares `"capabilities": []`, so
  `stado_fs_read` and `stado_fs_write` would be denied. The plugin
  doesn't need them; it only reads args + writes a result.

## Prerequisites

- Zig 0.15+ on `$PATH` (the build script was tested with 0.15.2; any
  toolchain that targets `wasm32-freestanding` works if you adapt the
  commands).
- stado on `$PATH`, or `STADO=/path/to/stado` in your environment.

## End-to-end walkthrough

From this directory:

```sh
# 1. One-time: generate the demo signer's Ed25519 keypair.
#    Writes the 32-byte seed to hello-demo.seed (chmod 0600) and
#    prints the public key + fingerprint you'll need below.
stado plugin gen-key hello-demo.seed

# 2. Compile + sign. build.sh runs zig, then `stado plugin sign`,
#    which rewrites plugin.manifest.json with the computed
#    wasm_sha256 + author_pubkey_fpr and emits plugin.manifest.sig.
./build.sh

# 3. Pin the demo public key (paste the "pubkey (hex)" value from
#    step 1). This is the one-time trust gate — stado refuses to
#    install a plugin signed by an unpinned key.
stado plugin trust <pubkey-hex> "stado example"

# 4. Verify + install. `verify` checks the signature, the wasm sha256,
#    rollback protection, CRL (if configured), and Rekor (if
#    configured). `install` runs the same checks then copies the
#    package into $XDG_DATA_HOME/stado/plugins/hello-0.1.0/.
stado plugin verify .
stado plugin install .

# 5. Run the tool.
stado plugin run hello-0.1.0 greet '{"name":"Ada"}'
# → {"message":"Hello, Ada!"}

stado plugin run hello-0.1.0 greet '{}'
# → {"message":"Hello, world!"}   (default name when none provided)
```

## How the ABI works in this plugin

Every tool call round-trips through four wasm functions:

| # | Caller | Callee            | Purpose                              |
|---|--------|-------------------|--------------------------------------|
| 1 | host   | `stado_alloc`     | allocate space for the args JSON      |
| 2 | host   | `stado_alloc`     | allocate space for the result buffer  |
| 3 | host   | `stado_tool_greet`| run the tool; write result, return N  |
| 4 | host   | `stado_free` ×2   | release both buffers after reading N  |

`stado_tool_greet`'s return value is the number of bytes written into
the result buffer, or `-1` on tool-side error. See
`src/hello.zig` for the exact shape.

## Iterating on the plugin

Edit `src/hello.zig`, then `./build.sh` to recompile + re-sign. The
wasm sha256 changes on every rebuild, so the install-time rollback
check will refuse to replace a newer-versioned install with an older
one — bump `version` in `plugin.manifest.json` (or uninstall the old
directory under `$XDG_DATA_HOME/stado/plugins/`) when you want to
replace an existing install.

`capabilities: []` in the manifest blocks every host capability gate
except `stado_log`. If you need filesystem access, add:

```json
"capabilities": [
  "fs:read:/absolute/path",
  "fs:write:/absolute/path"
]
```

Prefix-matched at host-import time — `stado_fs_read("/absolute/path/any/file")`
will be allowed, anything else returns `-1`.

## Cleaning up

```sh
# Uninstall the plugin.
rm -rf $XDG_DATA_HOME/stado/plugins/hello-0.1.0
# or, if XDG_DATA_HOME is unset:
rm -rf ~/.local/share/stado/plugins/hello-0.1.0

# Revoke trust for the demo signer.
stado plugin untrust <fingerprint>
```
