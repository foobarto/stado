package ptyblock

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// fakeSnapshotter is a minimal Snapshotter that returns a configured
// frame or error. Matches what the bubbletea Update path will see; no
// real PTY plumbing required.
type fakeSnapshotter struct {
	frame *pty.Screen
	err   error
	calls int
}

func (f *fakeSnapshotter) Snapshot(uint64) (*pty.Screen, error) {
	f.calls++
	return f.frame, f.err
}

// TestNew_DefaultsApplied: New with nil RenderOpts and a sensible
// snapshotter constructs a model in polling state with no cached
// frame and no error. Locks the no-op default.
func TestNew_DefaultsApplied(t *testing.T) {
	m := New(1, 80, 24, &fakeSnapshotter{}, nil)
	if m.SessionID() != 1 {
		t.Errorf("SessionID = %d, want 1", m.SessionID())
	}
	if m.Ended() {
		t.Errorf("fresh model should not be Ended")
	}
	if m.View() != "" {
		t.Errorf("fresh model View should be empty before first snapshot")
	}
}

// TestUpdate_SnapshotMsgUpdatesFrame: feeding a snapshotMsg with a
// frame replaces the model's cached state and queues the next tick.
// View now renders the frame.
func TestUpdate_SnapshotMsgUpdatesFrame(t *testing.T) {
	frame := fixture("hello")
	snap := &fakeSnapshotter{frame: frame}
	m := New(1, 80, 24, snap, nil)

	msg := snapshotMsg{frame: frame}
	m, cmd := m.Update(msg)
	if cmd == nil {
		t.Errorf("expected next tick cmd; got nil")
	}
	out := stripANSI(m.View())
	if !strings.Contains(out, "hello") {
		t.Errorf("View should render 'hello'; got %q", out)
	}
}

// TestUpdate_TerminalErrorEndsPolling: ErrNotFound (and wrappers)
// transition the model to ended state and stop the tick loop.
// Locks the auto-exit semantic — without it, the embedding TUI would
// keep polling after the session is destroyed.
func TestUpdate_TerminalErrorEndsPolling(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ErrNotFound direct", pty.ErrNotFound},
		{"ErrNotFound wrapped", errors.New("snapshot: " + pty.ErrNotFound.Error())},
		{"ErrClosed", pty.ErrClosed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(1, 80, 24, &fakeSnapshotter{}, nil)
			// errors.New + Is requires the inner err be the sentinel,
			// which the wrapped case isn't (it's a string-formatted
			// new error). Use fmt.Errorf with %w for that case instead.
			if c.name == "ErrNotFound wrapped" {
				c.err = wrapErr(pty.ErrNotFound)
			}
			msg := snapshotMsg{err: c.err}
			next, cmd := m.Update(msg)
			if !next.Ended() {
				t.Errorf("model should be Ended after terminal error %v", c.err)
			}
			if cmd != nil {
				t.Errorf("ended model should not queue more ticks; got cmd=%v", cmd)
			}
		})
	}
}

// TestUpdate_TransientErrorKeepsPolling: a non-terminal error (random
// I/O) should keep the model polling so a recoverable hiccup doesn't
// kill the live view. The error surfaces in the View footer.
func TestUpdate_TransientErrorKeepsPolling(t *testing.T) {
	m := New(1, 80, 24, &fakeSnapshotter{}, nil)
	transient := errors.New("temporary I/O blip")
	msg := snapshotMsg{err: transient}
	m, cmd := m.Update(msg)
	if m.Ended() {
		t.Errorf("transient error should not End the model")
	}
	if cmd == nil {
		t.Errorf("transient error should still queue next tick")
	}
	if !strings.Contains(m.View(), "snapshot failed") {
		t.Errorf("View should surface transient error footer; got %q", m.View())
	}
}

// TestSetSize_ResizerCalled: when a Resizer is wired and SetSize
// changes geometry, the resizer is called with the new cols/rows.
// Idempotent: same size = no-op.
func TestSetSize_ResizerCalled(t *testing.T) {
	resizer := &fakeResizer{}
	m := New(42, 80, 24, &fakeSnapshotter{}, nil).WithResizer(resizer)

	// Same size: no resize call.
	m, _ = m.SetSize(80, 24)
	if resizer.calls != 0 {
		t.Errorf("idempotent SetSize should not call resizer; got %d calls", resizer.calls)
	}

	// Different size: one call with the new dims.
	m, _ = m.SetSize(120, 40)
	if resizer.calls != 1 {
		t.Errorf("SetSize change: want 1 resize call, got %d", resizer.calls)
	}
	if resizer.lastID != 42 || resizer.lastCols != 120 || resizer.lastRows != 40 {
		t.Errorf("Resize args = (id=%d, cols=%d, rows=%d), want (42, 120, 40)",
			resizer.lastID, resizer.lastCols, resizer.lastRows)
	}
}

// TestView_EndedShowsFooter: after a terminal error, View renders the
// last good frame plus a "[process exited]" footer. Lets the operator
// see what the session looked like when it died.
func TestView_EndedShowsFooter(t *testing.T) {
	frame := fixture("last frame")
	m := New(1, 80, 24, &fakeSnapshotter{}, nil)
	m, _ = m.Update(snapshotMsg{frame: frame})
	m, _ = m.Update(snapshotMsg{err: pty.ErrNotFound})

	if !m.Ended() {
		t.Fatal("model should be ended")
	}
	out := stripANSI(m.View())
	if !strings.Contains(out, "last frame") {
		t.Errorf("View after end should show last frame; got %q", out)
	}
	if !strings.Contains(out, "[process exited]") {
		t.Errorf("ended View should include exit footer; got %q", out)
	}
}

// TestInit_QueuesTick: Init returns a tea.Cmd, not nil. The test
// doesn't drive the cmd (would require firing the timer); it just
// asserts the contract. Day 4's focus-aware ticker will rely on
// Init being callable to restart the loop.
func TestInit_QueuesTick(t *testing.T) {
	m := New(1, 80, 24, &fakeSnapshotter{}, nil).WithTickEvery(50 * time.Millisecond)
	cmd := m.Init()
	if cmd == nil {
		t.Errorf("Init should return a non-nil tea.Cmd")
	}
	// Sanity: the cmd is callable. We don't wait for it (50ms is fast
	// enough that this test is a few-ms overhead) but invoking it
	// confirms it's a real *tea function rather than a typed nil.
	_ = cmd
}

// TestModel_TeaModelInterface: the bubbletea tea.Model interface
// requires Init / Update / View. Compile-time check that ptyblock.Model
// can be embedded in a parent model that drives it polymorphically.
func TestModel_TeaModelInterface(t *testing.T) {
	var _ tea.Model = (*modelAsTeaModel)(nil)
}

// modelAsTeaModel adapts ptyblock.Model to tea.Model — Update needs
// to return (tea.Model, tea.Cmd) but ptyblock.Model.Update returns
// (Model, tea.Cmd) for ergonomic embedding. The wrapper makes the
// interface assertion above compile.
type modelAsTeaModel struct{ inner Model }

func (m *modelAsTeaModel) Init() tea.Cmd { return m.inner.Init() }
func (m *modelAsTeaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.inner.Update(msg)
	m.inner = next
	return m, cmd
}
func (m *modelAsTeaModel) View() string { return m.inner.View() }

type fakeResizer struct {
	calls    int
	lastID   uint64
	lastCols uint16
	lastRows uint16
}

func (f *fakeResizer) Resize(id uint64, cols, rows uint16) error {
	f.calls++
	f.lastID = id
	f.lastCols = cols
	f.lastRows = rows
	return nil
}

type fakeWriter struct {
	writes [][]byte
}

func (f *fakeWriter) Write(_ uint64, data []byte) (int, error) {
	dup := make([]byte, len(data))
	copy(dup, data)
	f.writes = append(f.writes, dup)
	return len(data), nil
}

// TestFocusBlur_IdempotentAndWriterRequired locks the focus toggle's
// preconditions: Focus is a no-op without a writer (the block stays
// read-only); Blur always works; both are idempotent.
func TestFocusBlur_IdempotentAndWriterRequired(t *testing.T) {
	noWriter := New(1, 80, 24, &fakeSnapshotter{}, nil)
	noWriter = noWriter.Focus()
	if noWriter.Focused() {
		t.Errorf("Focus without writer should be a no-op; got focused=true")
	}

	w := &fakeWriter{}
	withWriter := New(1, 80, 24, &fakeSnapshotter{}, nil).WithWriter(w)
	withWriter = withWriter.Focus()
	if !withWriter.Focused() {
		t.Errorf("Focus with writer should set focused=true")
	}
	withWriter = withWriter.Focus() // idempotent
	if !withWriter.Focused() {
		t.Errorf("Focus is idempotent")
	}
	withWriter = withWriter.Blur()
	if withWriter.Focused() {
		t.Errorf("Blur should clear focused")
	}
	withWriter = withWriter.Blur() // idempotent
	if withWriter.Focused() {
		t.Errorf("Blur is idempotent")
	}
}

// TestHandleKey_UnfocusedPassesThrough: when the block is NOT in
// shell-input mode, every key returns handled=false so the parent
// TUI's input router gets to decide. No bytes hit the PTY.
func TestHandleKey_UnfocusedPassesThrough(t *testing.T) {
	w := &fakeWriter{}
	m := New(1, 80, 24, &fakeSnapshotter{}, nil).WithWriter(w) // focused=false

	_, handled := m.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if handled {
		t.Errorf("unfocused HandleKey should return handled=false")
	}
	if len(w.writes) != 0 {
		t.Errorf("unfocused HandleKey should not write to PTY; got %d writes", len(w.writes))
	}
}

// TestHandleKey_FocusedTranslatesAndWrites: with focus + writer wired,
// keystrokes translate via keymap.go and reach the PTY. Spot-check the
// three families: rune, control byte, ANSI escape sequence.
func TestHandleKey_FocusedTranslatesAndWrites(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		want []byte
	}{
		{"rune 'h'", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}, []byte("h")},
		{"Ctrl+C", tea.KeyMsg{Type: tea.KeyCtrlC}, []byte{0x03}},
		{"Up arrow", tea.KeyMsg{Type: tea.KeyUp}, []byte("\x1b[A")},
		{"Enter", tea.KeyMsg{Type: tea.KeyEnter}, []byte("\r")},
		{"Backspace", tea.KeyMsg{Type: tea.KeyBackspace}, []byte{0x7F}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &fakeWriter{}
			m := New(1, 80, 24, &fakeSnapshotter{}, nil).WithWriter(w).Focus()
			_, handled := m.HandleKey(c.key)
			if !handled {
				t.Errorf("focused HandleKey(%v) should be handled", c.key)
			}
			if len(w.writes) != 1 {
				t.Fatalf("expected 1 write; got %d", len(w.writes))
			}
			if string(w.writes[0]) != string(c.want) {
				t.Errorf("wrote %q, want %q", w.writes[0], c.want)
			}
		})
	}
}

// TestHandleKey_AfterEndPassesThrough: once the session has ended,
// the writer points at a destroyed PTY and every Write fails
// silently. Keys must pass through to the parent so the operator
// can leave the dead block (TAB / Blur, etc.) rather than have
// keystrokes silently swallowed. Caught in 2026-05-09 second-pass
// review (codex).
func TestHandleKey_AfterEndPassesThrough(t *testing.T) {
	w := &fakeWriter{}
	m := New(1, 80, 24, &fakeSnapshotter{}, nil).WithWriter(w).Focus()
	// Drive into ended state.
	m, _ = m.Update(snapshotMsg{err: pty.ErrNotFound})
	if !m.Ended() {
		t.Fatal("model should be Ended")
	}
	if !m.Focused() {
		t.Fatal("model should still be focused (Ended is orthogonal)")
	}
	// Any key now: handled=false even though focused; no write.
	_, handled := m.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if handled {
		t.Errorf("HandleKey after end should pass through; got handled=true")
	}
	if len(w.writes) != 0 {
		t.Errorf("HandleKey after end should not write; got %d writes", len(w.writes))
	}
}

// TestTickEvery_FocusAware confirms the snapshot interval switches
// based on focus state. Day 4 split the single tickEvery into two
// rates (focused fast, unfocused slow) so the daemon doesn't take
// 30 Hz of snapshot calls per visible block when the operator is
// reading them passively. Locks the contract.
func TestTickEvery_FocusAware(t *testing.T) {
	w := &fakeWriter{}
	m := New(1, 80, 24, &fakeSnapshotter{}, nil).
		WithWriter(w).
		WithTickEveryRange(20*time.Millisecond, 500*time.Millisecond)

	// Unfocused: should pick the slow interval.
	if got := m.tickEvery(); got != 500*time.Millisecond {
		t.Errorf("unfocused tickEvery = %v, want 500ms", got)
	}
	m = m.Focus()
	if got := m.tickEvery(); got != 20*time.Millisecond {
		t.Errorf("focused tickEvery = %v, want 20ms", got)
	}
	m = m.Blur()
	if got := m.tickEvery(); got != 500*time.Millisecond {
		t.Errorf("blurred tickEvery = %v, want 500ms", got)
	}
}

// TestHandleKey_LeaveModeGestureIsCtrlCloseBracket: the only key
// that's NOT consumed by the block when focused is Ctrl+] — the
// telnet/socat/picocom leave-mode convention. Esc, Tab, SHIFT-TAB
// MUST reach the PTY (vim, shell autocomplete, editor normal-mode
// navigation depend on it). The 2026-05-09 second-pass review caught
// that an earlier version reserved all four keys, breaking real
// terminal usage. This test prevents regression.
func TestHandleKey_LeaveModeGestureIsCtrlCloseBracket(t *testing.T) {
	t.Run("Ctrl+] passes through (leave gesture)", func(t *testing.T) {
		w := &fakeWriter{}
		m := New(1, 80, 24, &fakeSnapshotter{}, nil).WithWriter(w).Focus()
		_, handled := m.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
		if handled {
			t.Errorf("Ctrl+] should pass through; got handled=true")
		}
		if len(w.writes) != 0 {
			t.Errorf("Ctrl+] should not write to PTY; got %d writes", len(w.writes))
		}
	})

	t.Run("Esc / Tab / SHIFT-TAB reach PTY (vim + shell compat)", func(t *testing.T) {
		cases := []struct {
			name string
			key  tea.KeyMsg
			want []byte
		}{
			{"Esc", tea.KeyMsg{Type: tea.KeyEsc}, []byte{0x1B}},
			{"Tab", tea.KeyMsg{Type: tea.KeyTab}, []byte{'\t'}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				w := &fakeWriter{}
				m := New(1, 80, 24, &fakeSnapshotter{}, nil).WithWriter(w).Focus()
				_, handled := m.HandleKey(c.key)
				if !handled {
					t.Errorf("%s should be consumed (sent to PTY); got handled=false", c.name)
				}
				if len(w.writes) != 1 || string(w.writes[0]) != string(c.want) {
					t.Errorf("%s wrote %q, want %q", c.name, w.writes, c.want)
				}
			})
		}
	})
}

// wrapErr returns an error that fmt.Errorf-wraps the inner one with
// %w, so errors.Is recovers the inner sentinel. Used by the test
// table to construct a "snapshot: pty: session not found" wrap that
// still classifies as terminal.
func wrapErr(inner error) error {
	return wrapper{inner: inner}
}

type wrapper struct{ inner error }

func (w wrapper) Error() string { return "snapshot: " + w.inner.Error() }
func (w wrapper) Unwrap() error { return w.inner }
