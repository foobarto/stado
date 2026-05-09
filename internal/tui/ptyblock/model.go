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
type Model struct {
	id         uint64       // PTY session id
	cols, rows int          // viewport geometry in cells
	snap       Snapshotter
	resizer    Resizer
	tickEvery  time.Duration

	// frame is the most recent snapshot. nil before the first tick;
	// View handles that case with an empty placeholder.
	frame *pty.Screen

	// state distinguishes polling from ended. Once ended, no more
	// ticks are queued; View() renders the last frame statically.
	state state

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

// New constructs a Model. tickEvery sets the snapshot refresh rate;
// 100 ms (10 Hz) is a sensible default for an unfocused block.
// Day 4 will rate-tune this based on focus state. id must reference a
// live PTY in snap; the model doesn't validate at construction (caller
// has just-spawned-id from `shell.spawn` and that contract is the
// caller's responsibility).
func New(id uint64, cols, rows int, snap Snapshotter, opts *RenderOpts) Model {
	tick := 100 * time.Millisecond
	var ro RenderOpts
	if opts != nil {
		ro = *opts
	}
	return Model{
		id:         id,
		cols:       cols,
		rows:       rows,
		snap:       snap,
		tickEvery:  tick,
		state:      statePolling,
		renderOpts: ro,
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

// WithTickEvery overrides the default refresh rate. Day 4's focus-
// aware ticker will set this dynamically; Day 2 callers can tune it
// for tests (a 1 ms tick + a fake clock makes assertion of redraws
// straightforward).
func (m Model) WithTickEvery(d time.Duration) Model {
	if d > 0 {
		m.tickEvery = d
	}
	return m
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

// tick returns a tea.Cmd that fires after m.tickEvery and produces a
// snapshotMsg. The cmd captures m.snap and m.id at scheduling time;
// the model's tick rate / snapshotter cannot change mid-tick (next
// tick picks up the new values).
func (m Model) tick() tea.Cmd {
	return tea.Tick(m.tickEvery, func(time.Time) tea.Msg {
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
