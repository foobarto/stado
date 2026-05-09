# render-demo-go

Minimal example exercising the `ui:render` capability + the
`stado_ui_render` host primitive end-to-end. The tool emits a single
panel cycling through every supported body kind (text / kv / list /
code / table / diff) so the operator can eyeball the renderer on
whichever channel they're on.

Per-channel rendering:

- **TUI**: bordered system block (rounded box-drawing chars) with
  per-kind body widgets (`internal/tui/panel_render.go`). Drops into
  the conversation flow next to other system blocks.
- **ACP**: `session/update kind=panel` notification (`internal/acp/
  render_bridge.go`). Clients that don't render panels yet can fall
  back to the panel's `title` + first text section.
- **MCP**: `CallToolResult.StructuredContent` carries the panels
  (`{panels: [...]}`); the result's text content also includes the
  ASCII rendering for clients that don't decode the structured
  field. (`cmd/stado/mcp_render_bridge.go`)
- **Headless** (`plugin.run` path): `session.update kind=panel`
  notification on the existing JSON-RPC stream
  (`internal/headless/render_bridge.go`).

Marked "manual test tool only" in its description so the model
won't try to invoke it on its own — install it only when you want
to manually exercise the render UI.

## Build, sign, install

```sh
cd plugins/examples/render-demo-go
stado plugin gen-key render-demo-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

## Use

In the TUI, run with the default panel:

```
/tool run render_demo
```

Or override the variant tag in the title bar:

```
/tool run render_demo {"variant":"warn"}
```

Variants: `info`, `ok`, `warn`, `error`, `recommendation`. Renderers
may colour the title or prefix accordingly; the structured payload
carries it through verbatim regardless of the channel's styling
support.

## What plugin authors learn from this

- The `stado_ui_render` host import returns `0` on success and `-n`
  on failure where `n` bytes of error message are at the result
  pointer. Read it back with `wasmBytes(resultPtr, -n)` to surface
  the host's structured reason ("ui:render cap missing" / "section
  N: ..." / etc.) — same negative-return convention every tool-
  bridging import in stado uses.
- Each section sets exactly one body field for its declared `kind`.
  The host validates this at decode and returns a structured error
  if a section carries the wrong body shape.
- Size caps: 64 KiB total payload, 32 KiB per section,
  200 rows × 16 cols on tables. Violations surface as decode-time
  errors.
- The pinned-allocation pattern (`pinned sync.Map` + `stado_alloc` /
  `stado_free`) keeps request buffers alive across the wasm
  invocation frame; copy payloads into pinned memory before passing
  pointers to the host.
