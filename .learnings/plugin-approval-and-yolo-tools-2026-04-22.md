Approval behavior changed materially:

- Automatic tool-call approval is gone. Tool execution is now yolo by default across TUI and headless surfaces.
- The remaining safety boundary for "a model may only execute tools it was actually offered this turn" is explicit enforcement in:
  - `internal/tui/model.go` via `turnAllowed` + `advanceToolQueue()`
  - `internal/runtime/runtime.go` via `allowedToolSet()` in `AgentLoop()`
- Plugins can request human approval explicitly through the `ui:approval` capability and the `stado_ui_approve` host import.
- The built-in `approval_demo` tool is intentionally a thin demo tool that exercises that plugin capability. In non-interactive surfaces it returns `approval UI unavailable` by design.

Live UAT notes:

- `bash` executes in the TUI without any approval popup.
- `approval_demo` opens the popup in the TUI, the draft input stays editable while it is pending, and `Up` + `Left/Right` + `Enter` resolves it correctly.
- A failed bundled wasm rebuild used to wipe `internal/builtinplugins/wasm/`; `internal/builtinplugins/build.sh` now builds into a temp dir first and only swaps outputs on success.
