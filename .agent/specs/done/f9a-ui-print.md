# F9a — `stado_ui_print` (TUI slice of F9)

Source: TODO.md F9. F9 has two new primitives — `stado_ui_print`
(plain text, fire-and-forget) and `stado_ui_render` (structured
panel). This spec covers `stado_ui_print` end-to-end on the TUI
surface only; render lands as F9b.

## What ships

`stado_ui_print(text, opts?)` — plugins emit plain text into the
operator's view. Fire-and-forget; no return value beyond
success/error (size violation, capability denial). New manifest
capability `ui:print` gates the import.

Wire format:

```
{
  "text": string (≤ 8 KiB)
  "severity": "info" | "warn" | "error"  (optional)
  "eol": bool (optional, default true)
  "stream_id": string (optional, ≤ 64 bytes)
}
```

## Acceptance criteria

1. `stado_ui_print` registered alongside `stado_ui_choose` /
   `stado_ui_approve`; gated by a new `ui:print` manifest cap.
2. Without the cap, the import logs a deny + returns -1.
3. Wire decode caps text at 8 KiB; rejects unknown severity values
   at decode time; size cap on `stream_id`.
4. TUI bridge appends a system-style block carrying the text and a
   severity tag the renderer can style. Severity colours match
   the existing system-block conventions where possible (warn /
   error fg from theme).
5. Non-TUI bridges (ACP / MCP / headless) succeed silently for
   now — fire-and-forget, channel disconnected = drop on the
   floor per F9 spec. Wiring proper non-TUI rendering is F9
   follow-on.
6. Existing `ui:choice` / `ui:approval` plugins unaffected.

## Non-goals (this slice)

- ACP `kind=text` payload: text events are already a thing on
  ACP, but routing print through them with the severity field
  ships in the F9 follow-on.
- MCP tool-result append for print: F9 follow-on.
- `stream_id` rendering as a coalesced block — for now the field
  is preserved but the TUI just emits one block per call.
- The umbrella `ui` capability that gates all three primitives —
  introduce it when render lands; mixing the umbrella in here
  would mean two breaking-changes for plugin authors instead of
  one.

## Design sketch

### Capability plumbing — `host.go`

Add `UIPrint bool` field and parse `ui:print` in the capability
parser switch. Mirror UIChoice exactly.

### Bridge interface — `host.go`

```go
type PrintBridge interface {
    Print(ctx context.Context, text string, opts PrintOpts) error
}

type PrintOpts struct {
    Severity string
    EOL      bool
    StreamID string
}
```

### Wire decode + import — `host_ui_print.go` (new)

Mirror `host_ui.go`'s register/decode pattern. Size caps for text
(8 KiB) and stream_id (64 bytes); severity validated against
{"", "info", "warn", "error"}.

### TUI bridge — `model_plugins.go`

`tuiPrintBridge` posts a `pluginPrintMsg` to the program; Update
handler appends a system block with the text + severity styling.

## Risk and self-critique

- *Why not just use `stado_log`?* Log is operator-debug oriented
  (severity-tagged stderr, ignored in headless). `stado_ui_print`
  is plugin → operator output in the active flow. Different
  audience, different rendering, different audit semantics.
- *Why fire-and-forget instead of a return code?* Spec called
  for non-blocking; a plugin emitting twenty progress lines
  doesn't want twenty round-trips through the host. Errors are
  size violations and cap denials only — both surface synchronously
  via the negative-return convention so the plugin still knows
  its emit was rejected.
- *What if a non-TUI surface gets a print?* For F9a we silently
  drop. Risk: a plugin author tests on TUI, ships, and operators
  on MCP see nothing. Acceptable for the slice — F9b ACP/MCP
  follow-on closes this gap; tests assert TUI works and document
  the constraint.

## Done definition

- All five new files: spec, host capability, host import, bridge
  interface, TUI handler.
- Tests cover: register/decode rejects malformed input; cap-deny
  returns -1; TUI bridge appends a system block; severity
  preserved.
- Spec moves to `.agent/specs/done/` after commit.
