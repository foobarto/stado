package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// TestSlashPaletteEnterDispatchesFullCommandWithArgs guards the bug where any
// slash command with a second argument was broken in the TUI: the inline slash
// palette captures every keystroke into its Query (the main textarea stays
// empty), and on Enter onPickerKey dispatched only the highlighted suggestion's
// NAME — so "/tool fs.read" ran "/tool" (args dropped) and "/monitor stop" /
// "/monitor <cmd>" did nothing. Enter must run the full typed command.
func TestSlashPaletteEnterDispatchesFullCommandWithArgs(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 30

	// Simulate the user having typed "/monitor stop": the palette is visible
	// and holds the text after the leading slash in its Query.
	m.slash.Open()
	m.slashInline = true
	m.slash.Query = "monitor stop"

	priorBlocks := len(m.blocks)
	onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.showHelp {
		t.Fatal("Enter ran the highlighted suggestion instead of the typed command — args dropped")
	}
	if len(m.blocks) <= priorBlocks {
		t.Fatal("Enter on '/monitor stop' produced no block — the command was swallowed")
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.body, "no active monitor") {
		t.Errorf("expected '/monitor stop' to dispatch with its argument (got %q)", last.body)
	}
	if m.slash.Visible {
		t.Error("the slash palette should close after dispatching")
	}
}

// TestSlashPaletteEnterStripsLeadingSlash: when the palette is opened via Ctrl+P
// and the user types the command WITH its leading slash (e.g. "/monitor stop"),
// the query carries that slash. Enter must dispatch "/monitor stop", not
// "//monitor stop". Guards the leading-slash trim.
func TestSlashPaletteEnterStripsLeadingSlash(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 30

	m.slash.Open()
	m.slashInline = true
	m.slash.Query = "/monitor stop" // leading slash, as typed via Ctrl+P

	priorBlocks := len(m.blocks)
	onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(m.blocks) <= priorBlocks {
		t.Fatal("Enter on '/monitor stop' (leading slash) produced no block")
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.body, "no active monitor") {
		t.Errorf("leading-slash query should dispatch '/monitor stop', got %q (likely '//monitor stop')", last.body)
	}
}

// TestSlashPaletteEnterUnknownCommandStillDispatches: a typed command with no
// matching suggestion (and no args) should still run so the user gets a proper
// "unknown command" response instead of Enter silently doing nothing.
func TestSlashPaletteEnterUnknownCommandStillDispatches(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 30

	m.slash.Open()
	m.slashInline = true
	m.slash.Query = "definitelynotacommand"
	// Force the match list empty so Selected() is nil (the no-match path).
	m.slash.Matches = nil

	priorBlocks := len(m.blocks)
	onPickerKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(m.blocks) <= priorBlocks {
		t.Fatal("Enter on an unknown slash command was swallowed (no feedback block)")
	}
}
