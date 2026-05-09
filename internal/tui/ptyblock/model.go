package ptyblock

import (
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// Snapshotter is the abstraction the block uses to fetch screen state.
// Production callers wire pty.Manager directly (it already implements
// `Snapshot(uint64) (*pty.Screen, error)`); the daemon-routed path
// can wrap a daemon.Client to satisfy the same shape; tests can mock.
//
// Decoupling here keeps Model unaware of the daemon vs. in-process
// distinction and makes the component testable without instantiating
// either.
type Snapshotter interface {
	Snapshot(id uint64) (*pty.Screen, error)
}

// Writer is the optional interface for sending keystrokes to a session.
// Day 2 (this commit) doesn't drive input yet — the block is read-only
// until Day 3 wires the TAB/SHIFT-TAB input router. Defining the
// interface now keeps the surface stable so Day 3 only needs to wire
// keypress handling, not change the constructor signature.
type Writer interface {
	Write(id uint64, data []byte) (int, error)
}

// Resizer is the optional interface for telling the underlying PTY a
// new geometry. The block calls this when its viewport size changes
// so the spawned process (vim, htop, etc.) re-flows. Day 2 wires the
// resize on geometry messages; the actual cell-grid output is still
// driven by snapshot ticks.
type Resizer interface {
	Resize(id uint64, cols, rows uint16) error
}

// Model is the bubbletea sub-model for one live PTY. Owns a session
// id + the snapshotter + render options. Polls Snapshot on a tick;
// renders via the package-level Render helper.
//
// Lifecycle: New() returns a model in the "polling" state with a tick
// already queued via Init(). The first View() shows whatever the
// initial Snapshot returned (blank for fresh sessions). Subsequent
// snapshotMsg events refresh the cached frame and queue the next tick.
//
// When the underlying session exits (Snapshot returns ErrNotFound or
// the manager reports closed), the model transitions to "ended"
// state and stops polling. View() shows the last good frame plus a
// muted "[process exited]" footer. The embedding TUI can dispose of
// the block at that point.
//
// Input routing (Day 3): when the embedding TUI calls Focus() the
// block enters "shell-input mode" — subsequent key events fed to
// HandleKey are translated to PTY bytes via keymap.go and written to
// the session's stdin via Writer. Blur() pops the mode. The parent
// TUI typically maps TAB to Focus and SHIFT-TAB / Esc to Blur as the
// gemini-cli-style enter/leave gesture. The mode toggle is
// orthogonal to the snapshot tick loop — focused blocks still poll;
// the only difference is that key events no longer pass through.
type Model struct {
	id         uint64 // PTY session id
	cols, rows int    // viewport geometry in cells
	snap       Snapshotter
	resizer    Resizer
	writer     Writer

	// Focus-aware refresh rates. tickEvery() picks one based on
	// whether the block is currently focused. Either can be tuned by
	// the embedding TUI via WithTickEvery (sets both to the same
	// value) or by directly assigning fields in tests.
	focusedTickEvery   time.Duration
	unfocusedTickEvery time.Duration

	// frame is the most recent snapshot. nil before the first tick;
	// View handles that case with an empty placeholder.
	frame *pty.Screen

	// state distinguishes polling from ended. Once ended, no more
	// ticks are queued; View() renders the last frame statically.
	state state

	// focused = shell-input mode active. HandleKey translates
	// keystrokes to PTY bytes and writes them when set; otherwise
	// HandleKey is a no-op that returns handled=false so the parent
	// TUI's normal input router gets the message.
	focused bool

	// renderOpts carries colour / cursor highlight choices.
	renderOpts RenderOpts

	// lastErr is the most recent non-fatal Snapshot error. Surfaced
	// in View as a muted footer so the operator sees "transient
	// snapshot failed" rather than a blank screen.
	lastErr error
}

type state int

const (
	statePolling state = iota
	stateEnded
)

// Default tick intervals. Focused = 33 ms (~30 Hz), the cadence htop
// and vim refresh at; the operator's eye sees fluid updates. Unfocused
// = 200 ms (5 Hz), enough to track long-running output without
// pegging the daemon's snapshot path. The two values together cap
// the daemon's snapshot rate at ~30 calls/sec per visible block,
// regardless of how the block is rendered.
const (
	defaultFocusedTickEvery   = 33 * time.Millisecond
	defaultUnfocusedTickEvery = 200 * time.Millisecond
)

// New constructs a Model. The snapshot refresh rate is focus-aware
// (30 Hz focused / 5 Hz unfocused) by default; WithTickEvery overrides
// for tests or special cases. id must reference a live PTY in snap;
// the model doesn't validate at construction (caller has just-spawned-
// id from `shell.spawn` and that contract is the caller's responsibility).
func New(id uint64, cols, rows int, snap Snapshotter, opts *RenderOpts) Model {
	var ro RenderOpts
	if opts != nil {
		ro = *opts
	}
	return Model{
		id:                  id,
		cols:                cols,
		rows:                rows,
		snap:                snap,
		focusedTickEvery:    defaultFocusedTickEvery,
		unfocusedTickEvery:  defaultUnfocusedTickEvery,
		state:               statePolling,
		renderOpts:          ro,
	}
}

// WithResizer attaches a Resizer so the block tells the PTY about
// geometry changes. Optional; without it the underlying process won't
// re-flow on TUI resize but rendering still works (the snapshot cell
// grid is just clamped to the block's MaxCols / MaxRows).
func (m Model) WithResizer(r Resizer) Model {
	m.resizer = r
	return m
}

// WithWriter attaches a Writer so the block can drive the PTY's
// stdin in shell-input mode (Day 3). Without a Writer, Focus() is a
// no-op (the block stays read-only) — keeps the panic-free contract
// for embedders that haven't wired input yet.
func (m Model) WithWriter(w Writer) Model {
	m.writer = w
	return m
}

// Focus enters shell-input mode. Subsequent HandleKey calls translate
// keystrokes to PTY bytes and write them. Idempotent. Without a
// Writer wired, Focus is a no-op (HandleKey stays read-only).
func (m Model) Focus() Model {
	if m.writer != nil {
		m.focused = true
	}
	return m
}

// Blur exits shell-input mode. Idempotent.
func (m Model) Blur() Model {
	m.focused = false
	return m
}

// Focused reports whether the block is currently consuming key input.
// The parent TUI uses this to decide whether to render the focus
// indicator (border highlight, status-bar mode marker).
func (m Model) Focused() bool { return m.focused }

// HandleKey is the input-mode entry point. When focused AND a writer
// is wired, translates the key to PTY bytes and writes to stdin;
// returns handled=true so the parent TUI's input router doesn't
// re-process the key. When unfocused, returns handled=false and the
// parent gets to handle it (e.g. TAB → m.Focus()).
//
// Reserved leave-mode gesture: Ctrl+]. The 2026-05-09 second-pass
// review (codex) caught that the original Esc / Tab / SHIFT-TAB
// reservation killed vim and shell autocomplete:
//
//   - vim users hit Esc constantly to exit insert mode; if Esc is
//     reserved by the parent TUI, every Esc kicks them out of the
//     PTY block instead.
//   - shell autocomplete is bound to TAB; reserving TAB for "leave
//     mode" means TAB never reaches the shell.
//   - SHIFT-TAB sends the xterm sequence \x1b[Z which vim and other
//     editors use in normal mode; reserving it breaks that too.
//
// Ctrl+] is the convention from telnet / socat / picocom: a key
// combination terminals never produce naturally, universally
// recognised as "escape to outer system." Operators in shell-input
// mode hit Ctrl+] to leave; everything else (Esc, Tab, SHIFT-TAB,
// arrows, function keys, control bytes) reaches the PTY untouched.
//
// Bytes that fail to write don't panic — the error is dropped
// silently because the snapshot tick will surface a "session ended"
// error within ~100 ms anyway, which is the right diagnostic for
// "the PTY went away" scenarios.
func (m Model) HandleKey(msg tea.KeyMsg) (Model, bool) {
	if !m.focused || m.writer == nil {
		return m, false
	}
	// After the session has ended, the writer points at a destroyed
	// PTY — every Write fails silently. Keys that would otherwise
	// route here should pass through to the parent so the operator
	// can leave the dead block rather than have their keystrokes
	// silently swallowed.
	if m.state == stateEnded {
		return m, false
	}
	// Ctrl+] is the always-passthrough leave gesture. Every other
	// key (including Esc, Tab, SHIFT-TAB) routes to the PTY.
	if msg.Type == tea.KeyCtrlCloseBracket {
		return m, false
	}
	bytes, ok := keyMsgToBytes(msg)
	if !ok {
		// Unrecognised key — let the parent decide what to do with
		// it (probably nothing). Returning false rather than swallowing
		// preserves operator agency.
		return m, false
	}
	_, _ = m.writer.Write(m.id, bytes)
	return m, true
}

// WithTickEvery overrides BOTH the focused and unfocused refresh
// rates to the same value. Useful in tests where a 1 ms tick plus a
// fake clock makes redraw timing predictable. For production tuning,
// use WithTickEveryRange to set the two rates independently.
func (m Model) WithTickEvery(d time.Duration) Model {
	if d > 0 {
		m.focusedTickEvery = d
		m.unfocusedTickEvery = d
	}
	return m
}

// WithTickEveryRange sets the focused and unfocused refresh rates
// independently. The block uses focused when shell-input mode is
// active, unfocused otherwise. Either zero falls back to the
// package default.
func (m Model) WithTickEveryRange(focused, unfocused time.Duration) Model {
	if focused > 0 {
		m.focusedTickEvery = focused
	}
	if unfocused > 0 {
		m.unfocusedTickEvery = unfocused
	}
	return m
}

// tickEvery picks the right interval for the current focus state.
func (m Model) tickEvery() time.Duration {
	if m.focused {
		return m.focusedTickEvery
	}
	return m.unfocusedTickEvery
}

// snapshotMsg is the bubbletea message produced by each tick. Carries
// either the new frame or the snapshot error (typed separately so
// session-ended is distinguishable from a transient lookup miss).
type snapshotMsg struct {
	frame *pty.Screen
	err   error
}

// Init queues the first snapshot tick. Bubbletea calls this when the
// model is mounted; later programmatic restarts (after session-ended,
// for example, if the embedding code wires a re-spawn) should also
// call Init to resume polling.
func (m Model) Init() tea.Cmd {
	return m.tick()
}

// tick returns a tea.Cmd that fires after the focus-aware interval
// and produces a snapshotMsg. The cmd captures m.snap and m.id at
// scheduling time; the model's tick rate / snapshotter cannot change
// mid-tick (next tick picks up the new values, including any focus
// state change since the last tick).
func (m Model) tick() tea.Cmd {
	interval := m.tickEvery()
	return tea.Tick(interval, func(time.Time) tea.Msg {
		frame, err := m.snap.Snapshot(m.id)
		return snapshotMsg{frame: frame, err: err}
	})
}

// Update consumes messages. Two paths matter:
//
//   - snapshotMsg: refresh the cached frame, queue next tick OR
//     transition to ended state on session-not-found / EOF errors.
//   - tea.WindowSizeMsg: re-pin the block's geometry and (when a
//     Resizer is wired) tell the PTY to re-flow.
//
// Other messages pass through untouched — the embedding TUI keeps
// driving its own state machine.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			if isTerminalError(msg.err) {
				m.state = stateEnded
				return m, nil
			}
			// Transient error — keep polling, surface in View.
			return m, m.tick()
		}
		m.lastErr = nil
		m.frame = msg.frame
		return m, m.tick()
	case tea.WindowSizeMsg:
		// Embedding TUI's WindowSizeMsg is the parent terminal size,
		// not the block's. Block geometry comes from SetSize calls;
		// we ignore the parent message here.
		return m, nil
	}
	return m, nil
}

// SetSize repositions the block's render area. Calls the Resizer (if
// wired) so the underlying PTY re-flows. Idempotent — same size is a
// no-op.
func (m Model) SetSize(cols, rows int) (Model, tea.Cmd) {
	if cols == m.cols && rows == m.rows {
		return m, nil
	}
	m.cols = cols
	m.rows = rows
	if m.resizer != nil && cols > 0 && rows > 0 {
		_ = m.resizer.Resize(m.id, uint16(cols), uint16(rows))
	}
	return m, nil
}

// View renders the cached frame to a styled string. Empty when no
// frame has arrived yet (the first tick hasn't fired). When the
// session has ended, appends a muted "[process exited]" footer; on
// transient snapshot error, appends "[snapshot failed: <err>]".
func (m Model) View() string {
	if m.frame == nil {
		// No frame yet, but a transient error already happened —
		// surface it so the operator sees "snapshot failed" instead
		// of a confusing blank block. Without this, an early-error
		// race (e.g. snapshotter wired to a stale id at boot) shows
		// nothing and looks like the block is just slow.
		if m.lastErr != nil {
			return fmt.Sprintf("[snapshot failed: %v]", m.lastErr)
		}
		return ""
	}
	opts := m.renderOpts
	if opts.MaxCols == 0 || opts.MaxCols > m.cols {
		opts.MaxCols = m.cols
	}
	if opts.MaxRows == 0 || opts.MaxRows > m.rows {
		opts.MaxRows = m.rows
	}
	body := Render(m.frame, &opts)
	if m.state == stateEnded {
		return body + "\n[process exited]"
	}
	if m.lastErr != nil {
		return body + fmt.Sprintf("\n[snapshot failed: %v]", m.lastErr)
	}
	return body
}

// Ended reports whether the session has terminated and the model
// stopped polling. The embedding TUI checks this to decide whether
// to keep the block around or dispose of it.
func (m Model) Ended() bool { return m.state == stateEnded }

// SessionID returns the underlying PTY id. Useful for the embedding
// TUI when it wants to wire input writes (Day 3) or surface "click
// to focus" affordances against the right session.
func (m Model) SessionID() uint64 { return m.id }

// isTerminalError classifies a snapshot error as session-ending.
// "pty: session not found" means the session has been destroyed
// (caller-initiated or process exit + reaper); "pty: session closed"
// likewise. Other errors are treated as transient.
func isTerminalError(err error) bool {
	if err == nil {
		return false
	}
	// pty.Manager exposes typed errors (ErrNotFound, ErrClosed) — use
	// errors.Is so a wrapping snapshot error like "snapshot: pty:
	// session not found" still classifies correctly.
	if errors.Is(err, pty.ErrNotFound) || errors.Is(err, pty.ErrClosed) {
		return true
	}
	return false
}
