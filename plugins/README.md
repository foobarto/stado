# Plugins

This tree now holds only the **bundled** plugins — the ones compiled into the
stado binary. The opt-in `optional/` and `demos/` lanes moved to a separate
distribution repo (see EP-0042); keeping compiled wasm out of the source tree.

| Lane | Where | Loaded by stado | Operator action |
|---|---|---|---|
| **Bundled** | [`bundled/`](bundled/) | Compiled into the stado binary at build time, available in every session | None — present once stado is installed |
| **Optional / demos** | [foobarto/stado-plugins](https://github.com/foobarto/stado-plugins) | Built standalone, signed, installed via `stado plugin install` | `stado plugin install github.com/foobarto/stado-plugins/<plugin>@<version>`; project copies under `.stado/plugins/` need `[plugins] allow_project_plugins = true` in user config |

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

## Optional plugins (moved)

The former `optional/` and `demos/` plugins — `browser`, `web-search`,
`persistent-shell`, `mcp-client`, the `hello*` examples, the approval-gate
demos, and the rest — now live in
[foobarto/stado-plugins](https://github.com/foobarto/stado-plugins), each
built + signed there and installable remotely:

```sh
stado plugin install github.com/foobarto/stado-plugins/<plugin>@<version>
```

First install from a remote identity runs **owner anchor TOFU**
(v0.70.0): the manifest must match the fetched `author.pubkey`, the
operator is prompted on first sight (or pass `--trust-anchor` for
non-interactive installs). Unreachable/missing anchor fails closed.
After rotation: `stado plugin untrust-anchor <host/owner>` then
reinstall. Local-directory installs still use `stado plugin trust` for
the signer key. See [docs/commands/plugin.md](../docs/commands/plugin.md).

## Where to start

- New to authoring plugins? See the `hello-go` example in
  [foobarto/stado-plugins](https://github.com/foobarto/stado-plugins) and
  [`docs/features/plugin-authoring.md`](../docs/features/plugin-authoring.md).
- Want to see the full host-import surface a plugin can call? Read
  [`docs/plugins/host-imports.md`](../docs/plugins/host-imports.md).
- Distributing your own plugins? See the offline-signing cookbook in
  [`SECURITY.md`](../SECURITY.md).
