`STADO_TUI_TRACE=1` logs can run inside `Model.Update` (for example on
the first Enter key path via `tuiTrace("input submit", ...)`).

Bubble Tea v1.3 uses an unbuffered `Program.msgs` channel, and
`Program.Send` writes directly to that channel. Calling `Program.Send`
synchronously from a log sink invoked inside `Model.Update` deadlocks:
the event loop is still executing Update and cannot receive the nested
message.

Fix pattern:

- log sinks that may run inside Update should mutate model-owned state
  directly under the model's own mutex
- any `Program.Send` used only to trigger a redraw must happen from a
  separate goroutine, never synchronously from the log call path
- regression-test the first Enter path with `STADO_TUI_TRACE=1` and an
  attached but non-running `tea.Program`; it should return quickly
