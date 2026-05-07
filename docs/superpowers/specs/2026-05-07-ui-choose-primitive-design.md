# `stado_ui_choose` host primitive — design

**Status**: draft, brainstormed in-session 2026-05-07.

## What ships

A new host primitive that lets wasm plugins prompt the operator for
a single or multi-choice answer through a bottom drawer in the TUI
(and through a structured ACP `session/update` event with a paired
client-response method when stado is running as an ACP server).

Plugin call (blocking until the user responds or cancels):

```
stado_ui_choose(req_ptr, req_len, resp_ptr, resp_cap) → int32
```

- **req** (JSON):
  ```json
  {
    "prompt":  "Which port should I scan?",
    "options": [
      {"id": "22",   "label": "SSH (22)"},
      {"id": "80",   "label": "HTTP (80)"},
      {"id": "443",  "label": "HTTPS (443)"}
    ],
    "multi":   false,
    "default": "443"
  }
  ```

- **resp** on success (positive int = bytes written):
  ```json
  {"selected": ["443"], "cancelled": false}
  ```
  or, if the user cancelled (Esc in TUI):
  ```json
  {"selected": [], "cancelled": true}
  ```

- **resp** on error: negative int via the existing tool-side-error
  wire format (`encodeToolSidePayload`). Error messages:
  - `"ui:choice cap missing"` — manifest didn't declare the cap
  - `"interactive UI unavailable"` — no bridge wired (headless / MCP)
  - `"choice request rejected"` — bridge returned an error

## Acceptance criteria

- Plugin manifests declaring `ui:choice` cap can call the primitive.
- Plugins WITHOUT the cap get a structured error result; their wasm
  module instantiates fine (registration is unconditional, gating
  is in the call body — same shape as `stado_ui_approve`).
- TUI surfaces the request as a bottom drawer with a list of
  options. Single-choice: arrow keys + Enter. Multi-choice: arrow
  keys + Space to toggle, Enter to confirm.
- Esc cancels in TUI (returns `cancelled=true` to the plugin).
- ACP server emits `session/update {"kind":"choice", ...}` and
  blocks on a corresponding `session/choice_response` method from
  the client. Cancellation = client never responds before
  `session/cancel`, OR client responds with `{"cancelled": true}`.
- Headless / MCP server return `"interactive UI unavailable"` —
  no fake fallback that picks `default` silently. The plugin must
  decide what to do without operator input.
- Operator-facing audit log (existing `slog` channel) records every
  request + response, plugin name, prompt, selected ids.
- Bidirectional cancellation: when the operator quits the TUI
  mid-request, the bridge propagates `cancelled=true` to the
  pending plugin call so it doesn't deadlock.

## Non-goals

- No streaming choices (e.g., live-filtered fuzzy picker). The
  static option list is what ships; richer pickers can compose
  on top of `tools__describe` style activation.
- No nested / hierarchical choices. Plugin should issue a second
  `stado_ui_choose` after the first resolves if it needs to drill
  in.
- No styling override from the plugin. Theme decides.
- No timeout. The plugin can post a `stado_progress` heartbeat if
  it wants the operator to see context, but no auto-default after
  N seconds. (User-side cancel is always available.)

## Design sketch

### Capability + manifest

New cap `ui:choice` parsed by `pluginRuntime.NewHost`. No
sub-suffix — just the bare cap. Operator-trust style: "this plugin
can prompt me to pick from a list".

### Host import

`internal/plugins/runtime/host_ui.go` gains
`registerUIChooseImport(builder, host)`. Mirrors the
`registerUIApprovalImport` shape:

```go
builder.NewFunctionBuilder().WithGoModuleFunction(...).
  Export("stado_ui_choose")
```

Caps gate at call-time. Reads the request, validates JSON, looks up
the bridge, makes a blocking call, encodes the response.

### Bridge interface

`internal/plugins/runtime/host.go` gains alongside `ApprovalBridge`:

```go
type ChoiceRequest struct {
    Prompt  string
    Options []ChoiceOption
    Multi   bool
    Default string  // optional id from Options
}

type ChoiceOption struct {
    ID    string
    Label string
}

type ChoiceResponse struct {
    Selected  []string
    Cancelled bool
}

type ChoiceBridge interface {
    RequestChoice(ctx context.Context, req ChoiceRequest) (ChoiceResponse, error)
}
```

`Host` gets a `ChoiceBridge` field. Wired by callers in
`internal/runtime/pluginrun/run.go` (parallel to
`attachLifecycleBridges`'s ApprovalBridge wiring).

### TUI implementation

`internal/tui/model.go` adds:

```go
choiceRequest *choiceRequest  // parallel to *approvalRequest
choiceCursor  int             // currently-highlighted option index
choiceMarked  map[string]bool // set of toggled-on ids in multi mode
```

Plumbing:
- A new tea message `pluginChoiceRequestMsg` carries
  `(ChoiceRequest, response chan ChoiceResponse)` to the model
  loop. Bridge implementation pushes the message via
  `bubbletea.Program.Send`.
- Render path: `renderChoiceDrawer(mainW)` — same
  bottom-pinned card as approvalCard, with:
  - title (⚠ icon + prompt)
  - option list (one row per option):
    - `▸ <label>` (cursor)
    - `[ ] <label>` / `[x] <label>` (multi mode)
    - tone-coloured row when current cursor
  - hint line (`↑/↓ navigate · Space toggle · Enter confirm · Esc cancel`)
- Keybindings in `model_update.go`:
  - ↑ / ↓ move cursor
  - Space toggles current option (multi only)
  - Enter:
    - single mode: emit selected = [options[cursor].ID]
    - multi mode: emit selected = sorted toggled ids
  - Esc: emit cancelled=true

### ACP integration

ACP server (`internal/acp/server.go`) gains:

```go
func (s *Server) bridgeForSession(sess *acpSession) plugins.ChoiceBridge
```

This bridge:
1. Allocates a new request id.
2. Sends `session/update {"kind":"choice", "request_id":..., ...}`.
3. Blocks waiting for a paired `session/choice_response`
   notification (new RPC method on the server).
4. Returns `ChoiceResponse{Selected: ids, Cancelled: cancelled}`.

`session/choice_response` is registered as a server-side method.
Param shape:
```json
{"sessionId": "...", "requestId": "...", "selected": ["443"], "cancelled": false}
```

The bridge correlates by request id, lookups the pending channel,
delivers, removes from the pending map. Session cancel signals all
pending choices to return cancelled=true.

### Headless / MCP

No bridge — the host import returns `"interactive UI unavailable"`.
The plugin gets a structured error and decides what to do (e.g.,
fall back to the request's `default`, or fail).

### Wire-format helpers

`internal/plugins/runtime/host_ui.go` reuses:
- `encodeToolSidePayload(mod, ptr, cap, payload)` for negative-error
  wire format.
- `writeBytes(mod, ptr, cap, payload)` for success payloads.

Request decoding uses `json.Unmarshal` after `readBytesLimited`.
Limits: prompt ≤ 4 KiB, options array ≤ 100 entries, label ≤ 256 B,
id ≤ 64 B. Reject with structured error if exceeded.

## Risk and self-critique

- **Reentrancy**: a plugin shouldn't be able to recurse into
  `stado_ui_choose` from inside its own choice handler — but plugins
  don't have callbacks here, the call is synchronous from the
  plugin's POV. Bridge-side: only one outstanding request per
  session. Subsequent requests while one is open: return error
  `"another choice already pending"`. (TUI matches: only one
  drawer at a time.)
- **JSON injection**: option labels go straight to the TUI. Already
  truncated; no markdown rendering — labels render verbatim. Good.
- **Long option lists**: 100-entry cap keeps the drawer
  navigable. Plugins needing more should redesign (page or filter
  on their own and present a shortlist).
- **Multi-mode default**: `default` field is single-id. For multi
  mode the plugin can pre-toggle by including the id in `default`
  as a comma-separated list — OR we could change the field type to
  `string | []string`. Picking: `default` is `[]string` always (a
  single id is encoded as a 1-element array), simpler shape. Update
  the wire format above to reflect this.
- **ACP bidirectional flow**: requires the ACP client to implement
  `session/choice_response`. Older clients would just leave the
  drawer hanging until session cancel. Document the protocol
  bump in CHANGELOG; ACP `ProtocolVersion` stays at 1 (additive
  notification kind, additive method — clients that don't know
  the kind ignore it; servers reply with `MethodNotFound` if the
  client never sends a response, which we already handle).
- **Cancellation propagation**: must trace ctx cancels (turn
  cancel, session cancel, ctrl-c) into the bridge. Bridge waits on
  `select { case resp := <-ch: ; case <-ctx.Done(): cancelled }`.

## Done definition

- New host import + cap parsing + tests (positive: cap declared,
  bridge wired, response routes; negative: cap missing, no bridge,
  malformed JSON, oversized inputs).
- TUI drawer renders single + multi modes, keybindings tested.
- ACP integration: bridge implementation + new method dispatcher
  + roundtrip test using a fake client.
- A bundled demo plugin (`approval_demo` style) exercising the
  primitive end-to-end so manual smoke covers the full path.
- CHANGELOG entry covering: new cap, new primitive, ACP method,
  TUI drawer.

## Phasing

Two commits:

1. **Phase A** — host import + cap + bridge interface + TUI
   drawer + tests + demo plugin. Headless / MCP return
   `"interactive UI unavailable"` cleanly.
2. **Phase B** — ACP integration: server-side bridge,
   `session/choice_response` method, roundtrip test.

Phase A delivers user value standalone (TUI is the primary
operator surface). Phase B unblocks ACP-driven editor flows and
follows once a real client wants it; design here so Phase B is
mostly mechanical.
