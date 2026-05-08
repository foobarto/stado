# F10 — Per-option editable field on stado_ui_choice

**Status: SHIPPED (TUI slice).** Stage 1 (schema) landed in commit
`ecfee36`. Stage 2 (TUI render + key handling) and Stage 3 (ACP/
MCP/headless rejection) ship together. All tests green. Pre-F10
callers unaffected. Wiring the new fields through the ACP
`kind=choice` payload is a separate follow-on slice tracked in
TODO.md F10's status note.

Source: TODO.md F10. Operator chose "F10 first, smallest slice" —
TUI surface only; ACP / MCP / headless deferred.

## What ships

`stado_ui_choose` accepts a richer per-option shape:

```
option = {
  id:        string  (existing; required)
  label?:    string  (existing; optional iff input present)
  prefix?:   string  (new; r/o decoration alongside the input field)
  input?: {           (new; optional editable field on this option)
    default:    string
    validator?: { kind: "length"|"regex"|"int"|"path"|"multiline", spec?: string }
  }
}
```

Response carries one new field — `input_value: string` (text typed
into the chosen option's input; "" when the chosen option had no
input). Existing callers (object options without `prefix`/`input`)
keep working unchanged.

## Acceptance criteria

1. **Wire compat.** A request body using the pre-F10 shape
   (`{prompt, options:[{id,label}], multi}`) decodes identically to
   pre-F10 behaviour. New fields are optional.
2. **Decode validation.** `decodeChooseRequest` rejects:
   - Validator `kind` outside the documented set.
   - Validator `regex` with a syntactically invalid Go RE2 pattern.
   - `prefix` exceeding the existing label byte limit.
   - `input.default` exceeding the existing label byte limit.
3. **Validators run runtime-side before returning.** The TUI
   re-prompts on validation failure; the operator never sees a
   validation error returned to the plugin (TUI-only slice — ACP/MCP
   reject with a structured error, see #6).
4. **TUI rendering.** A row with `input != nil` shows
   `prefix [editable] label` with the editable field highlighted on
   the cursor row. Tab toggles focus between the input field and
   the row chooser; Enter commits the row's current input value.
5. **Bare-input shortcut.** When `len(options) == 1` and that
   option has `input != nil` and `label == ""`, the drawer renders
   as a plain input prompt (no chooser scaffolding).
6. **ACP/MCP/headless rejection.** Until those channels grow input
   support, an option carrying `input` against a non-TUI bridge
   returns a structured error to the plugin
   (`"channel does not support input fields"`) instead of silently
   dropping the input.

## Non-goals (this slice)

- ACP `session/update kind=choice` payload extension.
- MCP tool-result extension.
- Headless decision-file extension.
- `multiline` validator widget — boolean flag accepted but rendered
  as the standard single-line input for now (multiline TUI widget is
  a follow-on).
- Auto-promotion of plain `string[]` options — current wire format
  uses `[{id, label}]`; F10's spec mention is forward-looking, not
  a back-compat task here.

## Design sketch

### Types — `internal/plugins/runtime/host.go`

```go
type ChoiceOption struct {
  ID    string
  Label string
  Prefix string         // F10
  Input *ChoiceInput    // F10; nil = pure choice row
}

type ChoiceInput struct {
  Default   string
  Validator *ChoiceValidator
}

type ChoiceValidator struct {
  Kind string // "length" | "regex" | "int" | "path" | "multiline"
  Spec string // kind-specific (e.g. "0,80" for length, the pattern for regex)
}

type ChoiceResponse struct {
  Selected   []string
  InputValue string  // F10
  Cancelled  bool
}
```

### Wire decode — `internal/plugins/runtime/host_ui.go`

Extended `chooseOptionWire` with `prefix` + `input` (using a
dedicated nested struct). `decodeChooseRequest` validates lengths
and validator kinds; rejects unknown kinds with a clear error.

### Validators — `internal/plugins/runtime/choice_validator.go` (new)

```go
func ValidateChoiceInput(input string, v *ChoiceValidator) error
```

Per-kind logic:
- `length`: spec `"min,max"` → byte-length window.
- `regex`: spec is a Go RE2 pattern; compile + `MatchString`.
- `int`: parse via `strconv.Atoi`.
- `path`: `filepath.IsLocal` + length cap.
- `multiline`: no validation (presence-only flag).

### TUI render — `internal/tui/model_render.go`

`renderChoiceRow` extended: when `opt.Input != nil`, render
`prefix [textfield] label`. Cursor row's textfield is in focus
mode (showing the typed value with a caret); other rows show the
default value flat. Bare-input case (single option, input only,
no label) renders as a dedicated single-line prompt instead of
calling `renderChoiceRow`.

### TUI key handling — `internal/tui/model_update.go`

In `stateChoice`, when the cursor sits on an option with `input`:
- Tab: toggle focus between the row's input field and the row
  selection (default: focus on the input).
- Printable runes / Backspace: edit the input.
- Enter: validate the current input; if valid, commit
  `ChoiceResponse{Selected: [id], InputValue: value}`; if invalid,
  flash an error line below the row and stay in the drawer.
- Esc: same cancel semantics as today.

### ACP / non-TUI rejection — `internal/acp/host.go`

`acpHost.RequestChoice` checks if any option carries `Input != nil`
and returns a structured error before delegating to the bridge.
Same gate applied in any other non-TUI choice bridge that ships.

## Risk and self-critique

**Possible objections.**

- *Wire format expanded twice for the same primitive — easy to
  accumulate cruft.* Counter: F10 is a deliberate consolidation
  (collapses the planned `stado_ui_input` into `choice`); the cost
  is a one-time wire extension, not an ongoing trickle.
- *TUI focus model (Tab between row vs input) is fiddly and we
  don't have it elsewhere.* Counter: the bare-input shortcut covers
  the common "just need a value" case; mixed choice+input rows are
  rarer, and the Tab toggle is the simplest disambiguator.
- *ACP/MCP rejection means a plugin can't author one prompt for
  both channels.* Acknowledged — that's why the rejection ships
  with an explicit structured error instead of silent fallback;
  plugins targeting both channels detect the error and fall back
  to plain choice. ACP/MCP support is a follow-on slice.
- *Validators run host-side but the spec lets them be advisory.*
  Strict: validation failures stay TUI-side; we never return them
  to the plugin (the plugin can't re-prompt — it would have to
  call `stado_ui_choose` again, defeating the round-trip
  reduction). The TUI re-prompts in place.

## Done definition

- All three stages land with passing tests.
- Existing `stado_ui_choose` tests (TUI choice drawer + bridge)
  continue to pass — wire compat verified.
- `go vet ./...` clean; `go build ./...` clean.
- Unreleased CHANGELOG entry under Plugins.
- Manifest capability `ui:choice` continues to gate; no new cap
  introduced (F9's `ui` umbrella cap is a separate spec).

## Stages

1. **Types + wire decode + validators.** Self-contained; no
   behavioural change because nothing wires the new fields.
   Lands first to anchor the schema.
2. **TUI render + key handling.** Activates the new fields end-to-end
   for the TUI bridge. Uses Stage-1 validators.
3. **ACP rejection.** Symmetric guard so non-TUI surfaces fail
   loudly until they grow input support.
