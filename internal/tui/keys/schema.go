package keys

// DefaultSchemaName is the schema used when none is configured. The emacs
// schema is the base layout — Defaults is its full keymap.
const DefaultSchemaName = "emacs"

// Schemas holds named keybinding schemas expressed as DELTAS over the emacs
// base (Defaults). Each value lists only the actions whose bindings differ
// from emacs; ResolveSchema overlays the delta on a copy of the base to
// produce the full keymap. A schema declared with an empty delta (emacs) is
// the base as-is.
//
// Adding a schema: add an entry here with only the actions that diverge from
// emacs. Everything omitted inherits the emacs binding.
var Schemas = map[string]map[Action]string{
	// emacs is the base layout; the delta is empty so it resolves to Defaults.
	"emacs": {},

	// vscode puts Home/End on the input line (where vscode users expect them)
	// and moves history-top/bottom to ctrl+home / ctrl+end. Word navigation
	// (ctrl+left/right) and ctrl+backspace are already emacs-default and need
	// no override here.
	"vscode": {
		InputLineHome: "home",
		InputLineEnd:  "end",
		MessagesFirst: "ctrl+home",
		MessagesLast:  "ctrl+end",
	},

	// vim is a MODAL schema (keymap Phase 2): its NORMAL/VISUAL behaviour is
	// driven by the pure engine in internal/tui/vimmode, dispatched in onKey
	// BEFORE the registry — not by per-key bindings here. So the schema delta
	// is empty: INSERT mode inherits the full emacs input editing (the editor
	// keymap stays emacs/readline-style, which is what a vim INSERT mode does
	// anyway), and the modal layer sits on top. The only schema-level remap —
	// ESC→NORMAL while INSERT — is handled in routing (vim-schema-only) rather
	// than as a binding swap, since ESC stays SessionInterrupt in every other
	// schema. See .agent/decisions/2026-06-13-keymap-phase2-modal-vim.md.
	"vim": {},
}

// ResolveSchema returns the full keymap for the named schema: a copy of the
// emacs base (Defaults) with the named schema's delta overlaid. An unknown or
// empty name resolves to the emacs base with no error — schema selection is a
// soft preference, not a hard gate. The returned map is a fresh copy; callers
// may mutate it without affecting Defaults or Schemas.
func ResolveSchema(name string) map[Action]string {
	out := make(map[Action]string, len(Defaults))
	for action, keysStr := range Defaults {
		out[action] = keysStr
	}
	for action, keysStr := range Schemas[name] {
		out[action] = keysStr
	}
	return out
}
