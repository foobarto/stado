package ptyblock

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// Integration tests for ptyblock — drives a small wrapper bubbletea
// app that hosts one PTYBlock against a real pty.Manager session,
// exercising the end-to-end loop:
//
//   spawn /bin/cat
//   → TAB (Focus)
//   → type a line
//   → snapshot tick fires, frame updates
//   → assert the typed text appears in the rendered output
//   → SHIFT-TAB (Blur)
//   → destroy
//
// These tests prove the package's contract holds inside a real
// bubbletea program loop, without integrating into stado's own TUI.
// The production TUI integration (wiring the block into the
// tool-result block when shell.spawn returns) is a follow-up — this
// harness validates the package is sound before that happens.

// wrapperModel is a minimal bubbletea program that hosts one PTYBlock
// plus a focus indicator. TAB → Focus, SHIFT-TAB → Blur, q → quit.
// Forwards key events to the block when focused; otherwise consumes
// the focus toggle and ignores everything else.
type wrapperModel struct {
	block PTYBlockShim
	width int
}

// PTYBlockShim is the embedding boundary the tests use. Wrapping the
// concrete Model lets us evolve its internal API without breaking
// the integration test contract: as long as Init/Update/View/HandleKey/
// Focus/Blur/Focused are intact, the wrapper still works.
type PTYBlockShim interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Model, tea.Cmd)
	View() string
	HandleKey(msg tea.KeyMsg) (Model, bool)
	Focus() Model
	Blur() Model
	Focused() bool
}

func (m wrapperModel) Init() tea.Cmd { return m.block.Init() }

func (m wrapperModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		// Quit on 'q' when unfocused.
		if !m.block.Focused() && msg.Type == tea.KeyRunes && len(msg.Runes) > 0 && msg.Runes[0] == 'q' {
			return m, tea.Quit
		}
		// Mode-toggle gestures take priority.
		switch msg.Type {
		case tea.KeyTab:
			if !m.block.Focused() {
				m.block = m.block.Focus()
				return m, nil
			}
		case tea.KeyShiftTab, tea.KeyEsc:
			if m.block.Focused() {
				m.block = m.block.Blur()
				return m, nil
			}
		}
		// Focused: forward to block. Block tells us if it consumed
		// the key; if not, we ignore (wrapper has no other use for
		// keystrokes beyond the toggle).
		if m.block.Focused() {
			next, _ := m.block.HandleKey(msg)
			m.block = next
			return m, nil
		}
		return m, nil
	}
	next, cmd := m.block.Update(msg)
	m.block = next
	return m, cmd
}

func (m wrapperModel) View() string {
	indicator := "[ READ ONLY ] press TAB to enter shell"
	if m.block.Focused() {
		indicator = "[ SHELL ] press SHIFT+TAB or Esc to leave"
	}
	return indicator + "\n" + m.block.View()
}

// TestIntegration_LiveCatRoundTrip drives the full path: real PTY via
// pty.Manager + ptyblock.Model + wrapper bubbletea program + teatest.
// Cat echoes stdin → stdout; the test types a marker, polls until
// the marker shows up in the rendered output, then exits cleanly.
func TestIntegration_LiveCatRoundTrip(t *testing.T) {
	// teatest output assertions need ANSI emission, force the profile.
	lipgloss.SetColorProfile(termenv.TrueColor)

	mgr := pty.NewManager()
	t.Cleanup(mgr.CloseAll)

	id, err := mgr.Spawn(pty.SpawnOpts{
		Argv: []string{"/bin/cat"},
		Cols: 80,
		Rows: 12,
	})
	if err != nil {
		t.Fatalf("spawn cat: %v", err)
	}
	if err := mgr.Attach(id, pty.AttachOpts{}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Build the block. Use a fast tick so the test doesn't sit
	// waiting 200 ms between snapshots.
	block := New(id, 80, 12, mgr, nil).
		WithWriter(mgr).
		WithResizer(mgr).
		WithTickEvery(20 * time.Millisecond)

	wrapper := wrapperModel{block: ptyBlockBox{Model: &block}}
	tm := teatest.NewTestModel(t, wrapper, teatest.WithInitialTermSize(80, 16))

	// 1. TAB to enter shell-input mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})

	// 2. Type "ping\r" — expect cat to echo it back into the snapshot.
	for _, r := range "ping" {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// 3. Wait for the marker to appear in the rendered output. The
	// snapshot tick + cat echo is on the order of tens of ms; give
	// 2 s as a generous CI ceiling.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "ping")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	// 4. SHIFT+TAB to leave shell-input mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyShiftTab})

	// 5. q to quit the wrapper.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	// 6. Drain final output — the wrapper exited via tea.Quit.
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	if final == nil {
		t.Fatal("FinalModel returned nil")
	}
	// The block should not have ended (cat is still running until
	// CloseAll in t.Cleanup tears it down).
	wm, ok := final.(wrapperModel)
	if !ok {
		t.Fatalf("FinalModel = %T, want wrapperModel", final)
	}
	if wm.block.Focused() {
		t.Errorf("wrapper exited focused — SHIFT+TAB should have blurred")
	}
}

// ptyBlockBox wraps *ptyblock.Model to satisfy the PTYBlockShim
// interface (which has value-receiver Update returning Model, not
// tea.Model). The wrapper updates the inner pointer in-place so the
// outer wrapperModel keeps a stable reference across ticks.
type ptyBlockBox struct {
	Model *Model
}

func (b ptyBlockBox) Init() tea.Cmd { return b.Model.Init() }
func (b ptyBlockBox) Update(msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := b.Model.Update(msg)
	*b.Model = next
	return next, cmd
}
func (b ptyBlockBox) View() string { return b.Model.View() }
func (b ptyBlockBox) HandleKey(msg tea.KeyMsg) (Model, bool) {
	next, handled := b.Model.HandleKey(msg)
	*b.Model = next
	return next, handled
}
func (b ptyBlockBox) Focus() Model {
	*b.Model = b.Model.Focus()
	return *b.Model
}
func (b ptyBlockBox) Blur() Model {
	*b.Model = b.Model.Blur()
	return *b.Model
}
func (b ptyBlockBox) Focused() bool { return b.Model.Focused() }
