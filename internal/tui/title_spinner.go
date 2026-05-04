package tui

// title_spinner.go — animated terminal-tab title.
//
// Many terminal emulators (kitty, alacritty, iTerm, Ghostty, Windows
// Terminal, GNOME Terminal, ...) display the OSC 0/2 window title in
// the tab strip. We use that to give visual "I'm working on it"
// feedback even when the user has switched to another window.
//
// Design:
//   - Idle: title = "stado"
//   - Streaming: title = "<braille-spinner-glyph> stado"  (cycles ~5fps)
//
// We poll on a tea.Tick instead of hooking every state-transition
// site because the tick is cheap (no work when title doesn't change)
// and a poll captures every variant of "we're busy" — including
// state == stateStreaming, m.compacting, and any future busy-flag
// without per-site plumbing.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// titleSpinnerInterval is the spinner cadence. 200ms = 5fps; fast
// enough to read as alive, slow enough to be friendly to log replays
// and screen-recording.
const titleSpinnerInterval = 200 * time.Millisecond

// titleSpinnerGlyphs are the braille-dot frames bashed into the
// canonical 10-frame Unicode spinner. Renders fine in every terminal
// emulator we support; falls back to "?" in TTYs without UTF-8 (rare
// for stado's target audience).
var titleSpinnerGlyphs = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// titleTickMsg fires every titleSpinnerInterval. Internal — never
// posted from outside this file.
type titleTickMsg struct{}

// titleTickCmd schedules the next spinner tick. Use as the result of
// Init() or a previous titleTickMsg.
func titleTickCmd() tea.Cmd {
	return tea.Tick(titleSpinnerInterval, func(time.Time) tea.Msg {
		return titleTickMsg{}
	})
}

// computeTitle returns the title string for the current model state.
// Pure function — Update calls it; tests can call it directly without
// constructing a tea.Program.
func (m *Model) computeTitle() string {
	if m.isBusy() {
		glyph := titleSpinnerGlyphs[m.titleSpinIdx%len(titleSpinnerGlyphs)]
		return string(glyph) + " stado"
	}
	return "stado"
}

// isBusy reports whether the title bar should show the spinner.
// Centralised so future "busy" flags (compaction, plugin install,
// long tool calls) can opt in by extending one predicate.
func (m *Model) isBusy() bool {
	return m.state == stateStreaming || m.compacting
}

// handleTitleTick advances the spinner index when busy and returns
// the commands to apply (a SetWindowTitle when the title changed,
// plus the next tick). Pure given the model state — keeps Update's
// switch arm tiny.
func (m *Model) handleTitleTick() tea.Cmd {
	if m.isBusy() {
		m.titleSpinIdx++
	}
	want := m.computeTitle()
	cmds := []tea.Cmd{titleTickCmd()}
	if want != m.lastTitle {
		m.lastTitle = want
		cmds = append(cmds, tea.SetWindowTitle(want))
	}
	return tea.Batch(cmds...)
}
