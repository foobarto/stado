# Plugins

All plugin source code lives under this tree. Three lanes:

| Lane | Where | Loaded by stado | Operator action |
|---|---|---|---|
| **Bundled** | [`bundled/`](bundled/) | Compiled into the stado binary at build time, available in every session | None — present once stado is installed |
| **Optional** | [`optional/`](optional/) | Built standalone, signed, installed via `stado plugin install` | Per-plugin opt-in |
| **Demos** | [`demos/`](demos/) | Plugin-author showcases and approval-gate test fixtures; not part of any user-facing surface | Install only when explicitly testing a host import or approval flow |

The three trees use the same plugin ABI; the difference is the
delivery channel. A plugin can move between lanes by relocating the
directory and updating the registration list (for bundled) or the
README index (for optional / demos).

## bundled/

What ships compiled into the stado binary. Includes the canonical
fs / shell / web / dns / agent surface, language tooling (LSP
wrappers, rg, ast-grep), and the auto-compact plugin that runs in
the background by default.

Build all bundled wasm into [`internal/plugins/bundled/wasm/`](../internal/plugins/bundled/wasm/)
(where Go's `//go:embed` picks them up at compile time):

```sh
bash plugins/bundled/build.sh
```

Adding a new bundled plugin:

1. Create `plugins/bundled/<name>/` with `main.go` (`//go:build wasip1`),
   importing `github.com/foobarto/stado/internal/plugins/bundled/sdk`
   for the alloc/free helpers.
2. Add the source dir to one of the lists in
   [`bundled/build.sh`](bundled/build.sh).
3. Register the tool(s) in
   [`internal/runtime/bundled_plugin_tools.go`](../internal/runtime/bundled_plugin_tools.go)
   and add the canonical-name metadata in
   [`internal/runtime/tool_metadata.go`](../internal/runtime/tool_metadata.go).
4. Rebuild bundled wasm and run `make build && ./stado install --force`.

## optional/

Standalone signed plugins shipped as user-facing tools. Install with:

```sh
cd plugins/optional/<plugin-name>
stado plugin gen-key <plugin-name>.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

Each subdirectory has its own `go.mod` (or `Cargo.toml` / `build.zig`)
so it builds as an independent module — wasm artefact + manifest +
signing happen per plugin. See
[`optional/README.md`](optional/README.md) for the per-plugin index.

## demos/

Plugin-author showcases that exist to validate one host-import or
one approval-flow path. Not meant for end users — they're either
trivial (`hello`, `hello-go`) or `// Manual test tool only` (the
approval-* and *-demo-go families).

Install procedure is identical to optional/, but you only do it
when you're actually exercising the surface the demo demonstrates.
See [`demos/README.md`](demos/README.md) for the index.

## Where to start

- New to authoring plugins? Start with
  [`demos/hello-go/`](demos/hello-go/) and
  [`docs/features/plugin-authoring.md`](../docs/features/plugin-authoring.md).
- Want to see the full host-import surface a plugin can call? Read
  [`docs/plugins/host-imports.md`](../docs/plugins/host-imports.md).
- Want to understand what's bundled vs. opt-in vs. demo? The tables
  in `bundled/`, `optional/`, and `demos/` READMEs are the source
  of truth.
