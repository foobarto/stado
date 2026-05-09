# F9b — `stado_ui_render` (structured panel)

Source: TODO.md F9. F9 introduces two new primitives — `stado_ui_print`
(plain text, fire-and-forget; F9a, shipped) and `stado_ui_render`
(structured panel; this spec). F9a covered the TUI surface for print
end-to-end. F9b covers the structured-panel primitive across host
import + plugin SDK + per-channel renderers (TUI / ACP / MCP /
headless), matching the ABI conventions documented in
`docs/plugins/host-imports.md`.

## What ships

`stado_ui_render(panel)` — fire-and-forget structured emit. Plugins
construct a typed payload (title + sections of typed body kinds) and
the runtime translates per-channel into the operator's view. No
return value beyond success/error (size cap, schema violation,
capability denial). New manifest capability `ui:render` gates the
import. Same shape as the existing `ui:print` / `ui:approval` /
`ui:choice` capability family — no umbrella `ui` cap in this slice
(see "Non-goals").

Wire format (per F9 design in `TODO.md`):

```jsonc
{
  "title":   string (≤ 80 chars, required),
  "sections": [
    {
      "kind":   "text" | "kv" | "list" | "code" | "table" | "diff",
      "body":   <kind-specific>,             // see below
      "heading": string (≤ 80 chars, optional)
    }
  ],
  "variant": "info" | "ok" | "warn" | "error" | "recommendation",  // optional
  "id":      string (≤ 64 bytes, optional — referenceable from a later choice),
  "footer":  string (≤ 200 chars, optional)
}
```

Per-section body shapes:

| `kind`  | `body` shape |
|---------|--------------|
| `text`  | `string` (markdown subset; ANSI/HTML/control chars stripped) |
| `kv`    | `[{label, value}]` (label ≤ 64; value ≤ 1 KiB) |
| `list`  | `{marker?: "bullet"\|"numbered"\|"check", items: [string]}` |
| `code`  | `{language?: string, content: string}` |
| `table` | `{columns: [string], rows: [[string]]}` (≤ 200 rows × ≤ 16 cols) |
| `diff`  | `{before: string, after: string}` |

Size caps enforced at the WASM boundary: 64 KiB total payload, 32 KiB
per section after JSON-decode. Validation failures surface via the
host's negative-return / written-message convention (see
`encodeToolSidePayload` — the same path AC1/AC2 traced).

## Acceptance criteria

Spec-level (any phase that claims AC4 = met must satisfy these):

1. **Host registration.** `stado_ui_render` registered alongside
   `stado_ui_print` / `stado_ui_choice` / `stado_ui_approve`; gated
   by a new `ui:render` manifest capability.
2. **Cap denial path.** Without the cap, the import returns `-n`
   with a payload body of `"stado_ui_render denied: insufficient
   capabilities (declare ui:render)"` — symmetric with
   `host_http_request.go` and `host_ui_print.go`.
3. **Size caps.** Total 64 KiB / per-section 32 KiB / table 200×16
   enforced at decode; violations return `-n` with a payload naming
   which cap was hit.
4. **Schema validation.** Unknown `kind` value, missing required
   field, oversized string, or wrong body shape returns `-n` with
   a payload naming the offending field.
5. **TUI rendering.** When `Host.RenderBridge != nil`, the panel
   appears as a bordered system-style block whose contents are
   rendered per body kind (text → wrapped paragraph, kv → aligned
   columns, list → marker-prefixed lines, code → monospace block
   with language tag, table → ascii grid, diff → before/after
   with `-`/`+` markers). Variant colour matches existing system-
   block conventions (info/ok/warn/error fg from theme). No layout
   changes to the input box, sidebar, or status bar.
6. **ACP rendering.** `session/update { kind: "panel", panel:
   <wire payload> }` notification emitted; ACP clients can
   re-render structured or fall back to a text representation
   constructed from the panel's title + first text section. Wire
   shape covered by `internal/acp/notifications_test.go`-style
   coverage.
7. **MCP rendering.** Tool-result envelope includes a `panels:
   [<wire payload>]` field alongside the existing `text` field;
   `mcp.ToolResult` round-trips the panel without lossy
   conversion. Backwards-compatible — pre-F9b clients ignore
   unknown fields.
8. **Headless rendering.** When `--ui-render-file <path>` is
   supplied, panels are appended one JSON envelope per line
   (NDJSON). Default (no flag): panels emit to stderr as JSON
   envelopes with a `[panel]` prefix line. Backwards-compatible
   — no impact on stdout.
9. **Plugin SDK helper.** `pkg/plugin-sdk-go/stado.UIRender(panel
   Panel) error` wraps the import + memory dance + readback so
   plugin authors don't reinvent it. Mirrors `UIPrint` (F9a) and
   `UIChoose` shape.
10. **Existing plugins unaffected.** No change to `ui:choice` /
    `ui:approval` / `ui:print` callers; manifest version of the
    bundled web/dns/etc. plugins NOT bumped.
11. **Backwards-compatible audit.** Panel payload recorded as the
    tool-exec event payload verbatim (per F9 design — replay
    re-renders against current channel; no rendered-snapshot
    serialisation).
12. **Documentation.** `docs/plugins/host-imports.md` adds a
    `stado_ui_render` row in §"Tier 1 — capability primitives"
    with signature + capability + size-cap notes; the manifest
    capability vocabulary table at the bottom adds `ui:render`.
    `TODO.md` F9 entry marked `~~RESOLVED~~` with a B5/B6-style
    note.

Phase-level (each phase below is independently shippable):

- **F9b.1** — host scaffolding only. Acceptance criteria 1-4 + 11.
  No renderers wired; surface emission no-ops per channel until
  later phases. Lets us land the wire contract + cap + size caps
  in one reviewable PR.
- **F9b.2** — TUI renderer. Adds AC 5. Visible end-to-end via
  `stado plugin run` against a demo plugin (`plugins/examples/
  render-demo-go`, modeled on `plugins/examples/choose-demo-go`).
- **F9b.3** — ACP wire. Adds AC 6.
- **F9b.4** — MCP wire. Adds AC 7.
- **F9b.5** — Headless wire. Adds AC 8.
- **F9b.6** — Plugin SDK helpers + docs. Adds AC 9, 12.
  AC 10 is verified at every phase boundary by re-running
  `internal/runtime/...` test suite.

## Non-goals (this slice)

- **`ui` umbrella capability.** F9a punted this with a note ("when
  render lands, introduce it"). On reflection, introducing the
  umbrella here would mean rewriting three already-shipped plugins
  (whatever uses `ui:print`, `ui:choice`, `ui:approval`) AND
  publishing a new ABI doc transition guide. The marginal value
  over per-primitive caps is small. Defer to a future
  consolidation spec; declare scope explicitly as out.
- **Streaming / replace updates.** Per F9 design — re-emit the
  panel for "progress"; use `print` with a shared `stream_id`
  (F9a) for continuations. Updates would require the panel to
  carry an `update_id` plus host-side render targeting; not in
  this slice.
- **In-panel interactivity.** Choice + render compose by the
  plugin emitting render then choice as separate calls. No
  buttons-inside-panel.
- **Plugin-controlled layout.** Body kinds are typed; the TUI
  picks layout. No flex / grid / custom CSS analogue.
- **Rich HTML in `text` bodies.** Strict markdown subset only,
  ANSI/HTML/control chars stripped. ACP / MCP carry text fields
  the same way today; no special escape-sequence carry.
- **Replay-from-snapshot.** Audit records the wire payload, not
  the rendered output. Replay re-renders against the current
  channel — same approach `ui:choice` audit takes today.

## Design sketch

### Capability plumbing — `internal/plugins/runtime/host.go`

Add `UIRender bool` to `Host`; capability parser switch already
has cases for `ui:print` / `ui:choice` / `ui:approval` — add
`ui:render` mirroring `ui:print` exactly. F9a is the precedent;
this is a one-line addition each in the cap parser + Host struct.

### Bridge interface — `internal/plugins/runtime/host.go`

```go
type RenderBridge interface {
    Render(ctx context.Context, panel Panel) error
}

type Panel struct {
    Title    string
    Sections []Section
    Variant  string // "" | "info" | "ok" | "warn" | "error" | "recommendation"
    ID       string
    Footer   string
}

type Section struct {
    Kind    string // "text" | "kv" | "list" | "code" | "table" | "diff"
    Heading string
    // Exactly one of the following set per Kind. Validated at decode.
    Text  string
    KV    []KVPair
    List  ListBody
    Code  CodeBody
    Table TableBody
    Diff  DiffBody
}
```

Mirrors `ChoiceBridge` / `PrintBridge` shape. Nil bridge =
fire-and-forget no-op (the channel-disconnected drop semantics
in F9a apply here too).

### Wire decode + import — `internal/plugins/runtime/host_ui_render.go` (new file)

Mirror `host_ui_print.go`'s register/decode pattern:
- Read args via `readBytesLimited(mod, argsPtr, argsLen,
  maxPluginRuntimeToolArgsBytes=64KiB)`.
- JSON-decode into a wire shape; validate per-section body matches
  declared `kind`; enforce size caps.
- On any failure: `encodeToolSidePayload` with a specific message
  ("section 3 kind=table: rows exceed 200" etc.).
- On success: dispatch to `host.RenderBridge.Render(ctx, panel)`;
  return 0.

### TUI bridge — `internal/tui/model_plugins.go`

`tuiRenderBridge.Render` posts a `pluginRenderMsg` carrying the
Panel; Update handler appends a system block whose template
renders the panel. New template
`internal/tui/render/templates/panel.tmpl`. Template uses existing
FuncMap (color, bg, bold, italic, underline, muted, wrap,
wrapHard, indent, markdown, marker) — no new helpers. Each body
kind compiles to a small per-kind sub-template.

### ACP wire — `internal/acp/notifications.go`

New notification kind `panel` paralleling existing `text`,
`tool_call`, `choice`. Payload is the wire JSON. Add to the
`session/update` enumeration in `cmd/stado/acp.go` --help text.

### MCP wire — `internal/runtime/mcp_glue.go` or
`cmd/stado/mcp_server.go`

Tool result envelope already carries `text`; add `panels []Panel`
alongside. Must not break pre-F9b clients (additive field; mcp-go
serialises additional fields without complaint per its struct-
encoder defaults).

### Headless wire — `internal/headless/server.go`

Panel emissions go to `--ui-render-file` (NDJSON append) when
supplied; default to stderr as `[panel] {...json...}`. New CLI
flag `--ui-render-file <path>` plumbed through `cmd/stado/
headless.go`.

### SDK helper — `pkg/plugin-sdk-go/stado/ui_render.go` (new)

```go
func UIRender(panel Panel) error
```

Marshals Panel → JSON, calls `stado_ui_render`, reads back the
error bytes via the negative-return convention. Mirrors
`UIPrint` and `UIChoose` shape so plugin authors get a familiar
API.

### Demo plugin — `plugins/examples/render-demo-go/`

Minimal Go plugin exercising every body kind. Pattern from
`plugins/examples/choose-demo-go`. Manifest declares only
`ui:render`. The demo's main.go cycles through one panel per
body kind so a `stado plugin run` against it visibly exercises
the renderer surface end-to-end.

## Risk and self-critique

- **Where this design might be wrong:** I'm treating the panel
  schema as fixed (`kind` enum, body shapes). If real plugin
  authors hit a layout case not covered (e.g. nested sections,
  inline images), they'll work around with `text` markdown until
  someone files an extension. Acceptable: the F9 design already
  declared "no plugin-controlled layout" as a non-goal, so the
  schema is deliberately small. Risk = real but contained.
- **Phase ordering risk.** F9b.1 (host scaffolding only) ships
  with no visible behaviour change; the operator can't tell it
  works without F9b.2. Mitigation: F9b.1 includes a unit test
  that exercises the import end-to-end with a fake
  `RenderBridge` capturing the Panel. That's the validation
  before F9b.2's TUI work.
- **MCP backwards compat.** Adding `panels` to the tool-result
  envelope risks breaking strict JSON-schema clients. Mitigation:
  document the additive field in `docs/commands/mcp-server.md`
  and the schema-version convention from F3 (TODO.md) means a
  bump signals the change to clients that pin schemas. Cross-
  check whether `schema_version` needs to bump for additive
  changes — F3's RESOLVED note says "additive=no bump", so safe.
- **Audit replay risk.** Recording the wire payload verbatim
  means replay re-renders against the *current* channel. If a
  panel was emitted to TUI then later replayed in headless, the
  user sees the JSON — not the rendered ascii grid. This matches
  how `ui:choice` audit works today; if it's a problem there it's
  a problem here, not a new regression.
- **Why not extend `ui:print` to carry structured fields?**
  Because F9a already shipped `ui:print` as plain text + severity
  + stream_id; bolting structured payloads onto it would mean
  three breaking shapes for one primitive. Render is a separate
  primitive on purpose.

## Done definition

- F9b.1 — F9b.6 each landed with their own tests; final commit
  flips TODO.md F9 to `~~RESOLVED~~`.
- `go test ./internal/plugins/runtime/... ./internal/runtime/...
  ./internal/tui/... ./internal/acp/... ./internal/headless/...
  ./cmd/stado/... -count=1` green at every phase boundary.
- `go build ./...` clean.
- `docs/plugins/host-imports.md` updated per AC 12; manifest
  capability vocabulary updated.
- `stado plugin run plugins/examples/render-demo-go` produces a
  visible panel per body kind (operator-verified smoke; no
  autonomous PTY harness for visual rendering).
