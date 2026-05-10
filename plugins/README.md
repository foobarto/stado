# Plugins

All plugin source code lives under this tree. Two flavors:

| Layer | Where | Loaded by stado | Operator action |
|---|---|---|---|
| **Bundled** | [`bundled/`](bundled/) | Compiled into the stado binary at build time, available in every session | None — present once stado is installed |
| **Optional** | [`optional/`](optional/) | Built standalone, signed, installed via `stado plugin install` | Per-plugin opt-in |

The two trees use the same plugin ABI; the only difference is the
delivery channel. A plugin under `optional/` could be promoted to
`bundled/` by adding it to [`bundled/build.sh`](bundled/build.sh) and
the registration list in
[`internal/runtime/bundled_plugin_tools.go`](../internal/runtime/bundled_plugin_tools.go);
a plugin under `bundled/` could be demoted by removing those entries
and shipping it through the install flow instead.

## bundled/

What ships compiled into the stado binary. Includes the canonical fs /
shell / web / dns / agent surface, language tooling (LSP wrappers, rg,
ast-grep), and the auto-compact plugin that runs in the background by
default.

Build all bundled wasm into [`internal/bundledplugins/wasm/`](../internal/bundledplugins/wasm/)
(where Go's `//go:embed` picks them up at compile time):

```sh
bash plugins/bundled/build.sh
```

Adding a new bundled plugin:

1. Create `plugins/bundled/<name>/` with `main.go` (`//go:build wasip1`),
   importing `github.com/foobarto/stado/internal/bundledplugins/sdk`
   for the alloc/free helpers.
2. Add the source dir to one of the lists in
   [`bundled/build.sh`](bundled/build.sh).
3. Register the tool(s) in
   [`internal/runtime/bundled_plugin_tools.go`](../internal/runtime/bundled_plugin_tools.go)
   and add the canonical-name metadata in
   [`internal/runtime/tool_metadata.go`](../internal/runtime/tool_metadata.go).
4. Rebuild bundled wasm and run `make build && ./stado install --force`.

## optional/

Standalone signed plugins. Install with:

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

## Where to start

- New to authoring plugins? Start with
  [`optional/hello-go/`](optional/hello-go/) and
  [`docs/features/plugin-authoring.md`](../docs/features/plugin-authoring.md).
- Want to see the full host-import surface a plugin can call? Read
  [`docs/plugins/host-imports.md`](../docs/plugins/host-imports.md).
- Want to understand what's bundled vs. opt-in? The tables in
  `bundled/` and `optional/` READMEs are the source of truth.
