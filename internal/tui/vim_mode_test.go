package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/internal/tui/vimmode"
	"github.com/foobarto/stado/pkg/agent"
)

// vimModel builds a Model on the vim keymap schema with the modal engine
// enabled, mirroring queueModel but selecting the vim schema.
func vimModel(t *testing.T) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistryForSchema("vim")
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.SetKeymapSchema("vim")
	m.width, m.height = 120, 30
	m.state = stateIdle
	return m
}

// TestVimModeEscEntersNormalMotionMovesCursorIReturnsInsert is the contract's
// required TUI test: with schema=vim, ESC enters NORMAL, a motion moves the
// cursor, and `i` returns to INSERT.
func TestVimModeEscEntersNormalMotionMovesCursorIReturnsInsert(t *testing.T) {
	m := vimModel(t)

	// Launch posture: INSERT.
	if m.vim.Mode() != vimmode.ModeInsert {
		t.Fatalf("launch mode = %v, want INSERT", m.vim.Mode())
	}

	// Seed the buffer and park the cursor at the end (INSERT-style).
	m.input.SetValue("hello")
	if got := m.input.CursorOffset(); got != 5 {
		t.Fatalf("setup: cursor = %d, want 5 (end of \"hello\")", got)
	}

	// ESC -> NORMAL.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Fatalf("after ESC, mode = %v, want NORMAL", m.vim.Mode())
	}

	// `0` motion -> cursor to line start (byte 0).
	_, _ = m.Update(tea.KeyPressMsg{Text: "0"})
	if got := m.input.CursorOffset(); got != 0 {
		t.Errorf("after `0`, cursor = %d, want 0", got)
	}

	// `l` motion -> cursor right one rune.
	_, _ = m.Update(tea.KeyPressMsg{Text: "l"})
	if got := m.input.CursorOffset(); got != 1 {
		t.Errorf("after `l`, cursor = %d, want 1", got)
	}

	// Motion keys must NOT have been typed into the buffer.
	if got := m.input.Value(); got != "hello" {
		t.Errorf("buffer mutated by NORMAL-mode motions: %q, want \"hello\"", got)
	}

	// `i` -> back to INSERT.
	_, _ = m.Update(tea.KeyPressMsg{Text: "i"})
	if m.vim.Mode() != vimmode.ModeInsert {
		t.Errorf("after `i`, mode = %v, want INSERT", m.vim.Mode())
	}

	// In INSERT, typing inserts into the buffer (cursor was at 1).
	_, _ = m.Update(tea.KeyPressMsg{Text: "X"})
	if got := m.input.Value(); got != "hXello" {
		t.Errorf("after INSERT type X, buffer = %q, want \"hXello\"", got)
	}
}

// TestVimModeNormalEscIsNoOp: ESC in NORMAL mode does nothing — it does NOT
// trigger SessionInterrupt (the decision: interrupt is Ctrl+G only).
func TestVimModeNormalEscIsNoOp(t *testing.T) {
	m := vimModel(t)
	// Arrange an in-flight stream so SessionInterrupt would be observable.
	cancelled := false
	m.state = stateStreaming
	m.streamCancel = func() { cancelled = true }

	// Enter NORMAL.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Fatalf("after first ESC, mode = %v, want NORMAL", m.vim.Mode())
	}
	// ESC again in NORMAL — must be a no-op (no stream cancel).
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cancelled {
		t.Errorf("ESC in NORMAL triggered SessionInterrupt — must be a no-op")
	}
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Errorf("ESC in NORMAL changed mode to %v, want NORMAL (no-op)", m.vim.Mode())
	}
}

// TestVimModeCtrlGStillInterrupts: Ctrl+G keeps its SessionInterrupt binding in
// vim NORMAL mode — the kill switch survives the modal layer.
func TestVimModeCtrlGStillInterrupts(t *testing.T) {
	m := vimModel(t)
	cancelled := false
	m.state = stateStreaming
	m.streamCancel = func() { cancelled = true }

	// Enter NORMAL, then Ctrl+G.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	_, _ = m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !cancelled {
		t.Errorf("Ctrl+G in vim NORMAL did not interrupt the stream")
	}
}

// TestEmacsSchemaEscStillInterrupts is the regression guard: with the emacs
// schema, ESC still triggers SessionInterrupt and no modal dispatch occurs.
func TestEmacsSchemaEscStillInterrupts(t *testing.T) {
	m := queueModel(t) // emacs (default) schema, vim disabled
	if m.vimEnabled {
		t.Fatalf("queueModel should not enable vim")
	}
	cancelled := false
	m.state = stateStreaming
	m.streamCancel = func() { cancelled = true }

	// Type some text, then ESC — must cancel the stream (SessionInterrupt),
	// not enter a modal NORMAL state.
	m.input.SetValue("hello")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !cancelled {
		t.Errorf("emacs schema: ESC did not trigger SessionInterrupt")
	}
	// vim engine must remain inert (still INSERT, since vimEnabled is false the
	// dispatch never ran).
	if m.vim.Mode() != vimmode.ModeInsert {
		t.Errorf("emacs schema: vim engine mode changed to %v — modal dispatch leaked", m.vim.Mode())
	}
	// A subsequent motion key must be typed as text (no modal interception).
	m.input.SetValue("")
	_, _ = m.Update(tea.KeyPressMsg{Text: "j"})
	if got := m.input.Value(); got != "j" {
		t.Errorf("emacs schema: `j` not typed as text (got %q) — modal dispatch leaked", got)
	}
}

// TestVimModeDeleteWordViaTUI exercises an operator+motion end-to-end through
// the real Update loop, confirming the engine result is applied to the editor
// via SetValueWithCursor.
func TestVimModeDeleteWordViaTUI(t *testing.T) {
	m := vimModel(t)
	m.input.SetValue("foo bar")

	// ESC to NORMAL, `0` to line start, then `d` `w` (delete word).
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	_, _ = m.Update(tea.KeyPressMsg{Text: "0"})
	_, _ = m.Update(tea.KeyPressMsg{Text: "d"})
	_, _ = m.Update(tea.KeyPressMsg{Text: "w"})

	if got := m.input.Value(); got != "bar" {
		t.Errorf("dw via TUI: buffer = %q, want \"bar\"", got)
	}
	if got := m.input.CursorOffset(); got != 0 {
		t.Errorf("dw via TUI: cursor = %d, want 0", got)
	}
}

// TestVimModeSubmitResetsToInsert: submitting a prompt from NORMAL mode returns
// the engine to INSERT, ready for the next line.
func TestVimModeSubmitResetsToInsert(t *testing.T) {
	m := vimModel(t)
	// A slash command submits without needing a provider/stream (routes to
	// handleSlash), which keeps the test hermetic while still exercising the
	// reset-to-INSERT chokepoint in submitInput.
	m.input.SetValue("/clear")

	// ESC to NORMAL.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Fatalf("setup: mode = %v, want NORMAL", m.vim.Mode())
	}
	// Enter submits (falls through to InputSubmit from NORMAL).
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.vim.Mode() != vimmode.ModeInsert {
		t.Errorf("after submit, mode = %v, want INSERT", m.vim.Mode())
	}
	if m.input.Value() != "" {
		t.Errorf("after submit, input not reset: %q", m.input.Value())
	}
}

// TestVimModeNormalCtrlXChordSuffixNotEatenByVim guards the Codex-flagged P2:
// in NORMAL mode a pending Ctrl+X prefix's suffix key (`a`/`g`/`l`/...) must
// complete the chord, NOT be consumed by the vim engine as a command. `ctrl+x
// a` is the AgentSwitch chord; vim's bare `a` would switch to INSERT, so if the
// suffix were eaten the mode would flip — assert it stays NORMAL.
func TestVimModeNormalCtrlXChordSuffixNotEatenByVim(t *testing.T) {
	m := vimModel(t)
	m.input.SetValue("hello")

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Fatalf("setup: mode = %v, want NORMAL", m.vim.Mode())
	}

	// Ctrl+X records the prefix; the next bare key must complete `ctrl+x a`.
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Text: "a"})

	if m.vim.Mode() != vimmode.ModeNormal {
		t.Errorf("ctrl+x suffix `a` was eaten by the vim engine (mode=%v) — prefix chord broken in NORMAL", m.vim.Mode())
	}
	if got := m.input.Value(); got != "hello" {
		t.Errorf("ctrl+x a mutated the buffer: %q, want \"hello\"", got)
	}
}

// TestVimModeNormalChordDoesNotEditBuffer guards the second Codex-flagged P2:
// in NORMAL mode an unhandled editor chord (ctrl+w/u/k/a/e readline editing)
// must NOT fall through to the textarea and edit the buffer. ESC then Ctrl+W
// previously deleted a word.
func TestVimModeNormalChordDoesNotEditBuffer(t *testing.T) {
	m := vimModel(t)
	m.input.SetValue("hello world")

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Fatalf("setup: mode = %v, want NORMAL", m.vim.Mode())
	}

	// Ctrl+W in NORMAL must be swallowed, not routed to readline delete-word.
	_, _ = m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if got := m.input.Value(); got != "hello world" {
		t.Errorf("ctrl+w in NORMAL edited the buffer (readline leak): %q, want \"hello world\"", got)
	}
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Errorf("ctrl+w changed mode to %v, want NORMAL", m.vim.Mode())
	}
}

// TestVimModeNormalFunctionalKeyReachesAppBinding guards the third Codex P2:
// non-text functional keys (tab/shift+tab/pageup/home/...) must reach their
// app-level bindings in NORMAL mode, not be swallowed by the engine's
// catch-all. `tab` is ModeToggle (Plan/Do) — assert it flips the mode.
func TestVimModeNormalFunctionalKeyReachesAppBinding(t *testing.T) {
	m := vimModel(t)
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc}) // -> NORMAL
	if m.vim.Mode() != vimmode.ModeNormal {
		t.Fatalf("setup: mode = %v, want NORMAL", m.vim.Mode())
	}
	before := m.mode
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.mode == before {
		t.Errorf("tab in vim NORMAL did not reach the ModeToggle binding (mode still %v) — functional key eaten by the engine", m.mode)
	}
}
