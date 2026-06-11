// Package treepicker renders the in-TUI session forest as a navigable,
// collapsible tree modal. It mirrors the taskpicker / sessionpicker idiom:
// a Visible modal with an Update(msg)→(cmd,bool) loop and an outbox the
// host drains via TakeAction.
//
// The package holds only strings + scroll/expansion state. It never imports
// internal/runtime or internal/tui — the host flattens the *runtime.Forest
// into a slice of Node values (plain data) and pushes them in via Open. This
// keeps the package free of an import cycle with tui and trivially testable
// without a git sidecar.
//
// This increment ships NAVIGATION + SWITCH only (build stages 3-4 of the
// /tree design). Peek and branch wiring (stages 5-6) are deliberately out of
// scope: the keymap leaves room for them but Enter on a session row is the
// only action that emits a Command today.
package treepicker

import (
	tea "charm.land/bubbletea/v2"
)

// Avail mirrors runtime.Availability as a plain string enum so the package
// stays free of a runtime import. The host maps runtime.Availability onto
// these verbatim. The values drive only the status glyph + colour.
type Avail string

const (
	// AvailLive — worktree present, owning pid alive.
	AvailLive Avail = "live"
	// AvailIdle — worktree present, no live pid.
	AvailIdle Avail = "idle"
	// AvailDetached — refs only, no worktree. Switch is gated off.
	AvailDetached Avail = "detached"
)

// Node is one row of input the host pushes in: a session in the forest,
// flattened to plain data. The host computes Depth/ParentTurn/Orphan from the
// runtime.Forest; the picker only renders + navigates. TurnCount drives the
// expand affordance (a session with turns can be expanded to a — currently
// cosmetic — turn list; turn rows are not yet selectable in this increment,
// so expansion just reveals them for context).
type Node struct {
	// ID is the session id. Used as the switch target and the stable key the
	// cursor re-pins to across a rebuild.
	ID string
	// Label is the human-facing session name (description or short id).
	Label string
	// Meta is the right-aligned column: status + turns + last-active etc.
	Meta string
	// Depth is the BFS depth from the forest root (root == 0). Drives indent.
	Depth int
	// Avail gates the switch action and selects the status glyph.
	Avail Avail
	// IsCurrent marks the you-are-here session (▸ marker).
	IsCurrent bool
	// ParentTurn is the parent turn this session forked at, or -1 when the
	// fork point matched no turn tag (mid-turn / unlinked) — drives the
	// "⑂ turn N" / "⑂ unlinked" origin tag. Only rendered when HasParent.
	ParentTurn int
	// HasParent is true when this node is a fork edge (renders an origin tag).
	HasParent bool
	// Orphan marks a node whose parent refs are gone (renders "⑂ orphan").
	Orphan bool
	// TurnCount drives the expand affordance + the expanded turn list.
	TurnCount int
	// Turns are the per-turn summary lines shown when the node is expanded.
	// Plain strings — the host pre-renders them (e.g. "turn 3 · fix parser").
	// Not selectable in this increment; shown for context only.
	Turns []string
}

// CommandType enumerates the actions the picker can emit to the host.
type CommandType int

const (
	// CommandNone — no action this Update (navigation, expand/collapse).
	CommandNone CommandType = iota
	// CommandSwitch — Enter over a switchable session row. Host calls
	// switchToSession(ID) and closes the picker.
	CommandSwitch
)

// Command is the picker's outbox payload. The host drains it via TakeAction
// after each handled Update.
type Command struct {
	Type CommandType
	ID   string
}

// row is one line in the flattened DFS: either a session header row or one of
// its expanded turn rows. Only session rows carry a node index and are
// landable by the cursor; turn rows are skipped during navigation. Rows carry
// structure, not rendered text — View renders each visible row fresh every
// frame so the selection highlight tracks the cursor without a rebuild
// (mirrors the taskpicker/palette render-on-View idiom).
type row struct {
	nodeIdx  int    // index into Model.nodes, or -1 for a turn row
	turnText string // the pre-rendered turn summary (turn rows only)
}

// Model is the treepicker modal. It owns the forest's flattened rows, the
// cursor, per-session expansion state, and an outbox Command.
type Model struct {
	Visible bool
	Width   int
	Height  int

	nodes    []Node
	rows     []row
	cursor   int             // index into rows; always points at a session row
	expanded map[string]bool // session id -> expanded
	out      Command
}

// New returns an empty, hidden picker.
func New() *Model { return &Model{expanded: map[string]bool{}} }

// Open shows the picker over the supplied flattened forest. currentID, when
// present, is selected and its ancestry auto-expanded so the working lineage
// is visible on open.
func (m *Model) Open(nodes []Node, currentID string) {
	m.Visible = true
	m.nodes = append([]Node(nil), nodes...)
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	// Auto-expand the current session so its turns are visible, and pin the
	// cursor to it. (Lineage auto-expansion is a stage-7 polish item; here we
	// expand just the current node, which is the common case.)
	if currentID != "" {
		m.expanded[currentID] = true
	}
	m.rebuildRows()
	m.cursor = 0
	if currentID != "" {
		m.selectID(currentID)
	}
}

// Close hides the picker. Expansion state is retained so re-opening lands in
// the same shape.
func (m *Model) Close() { m.Visible = false }

// TakeAction returns the pending Command and clears the outbox. The host
// calls this after a handled Update to act on a switch.
func (m *Model) TakeAction() Command {
	c := m.out
	m.out = Command{}
	return c
}

// SelectedID returns the session id under the cursor, or "" when there is no
// selectable row.
func (m *Model) SelectedID() string {
	if n := m.selectedNode(); n != nil {
		return n.ID
	}
	return ""
}

func (m *Model) selectedNode() *Node {
	if !m.Visible || len(m.rows) == 0 {
		return nil
	}
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	r := m.rows[m.cursor]
	if r.nodeIdx < 0 || r.nodeIdx >= len(m.nodes) {
		return nil
	}
	return &m.nodes[r.nodeIdx]
}

// Update consumes a keypress while Visible. Returns (cmd, handled); handled
// means the host must short-circuit so the keystroke doesn't leak to the
// textarea beneath. The tree-layer keymap (navigation + switch only):
//
//	↑/k ↓/j   move (clamp, no wrap)
//	→/l       expand the cursor's session
//	←/h       collapse the cursor's session
//	g / G     home / end
//	enter     switch to the cursor's session (if switchable)
//	esc/ctrl+c close
func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch km.String() {
	case "up", "k":
		m.moveCursor(-1)
		return nil, true
	case "down", "j":
		m.moveCursor(1)
		return nil, true
	case "right", "l":
		m.expandSelected()
		return nil, true
	case "left", "h":
		m.collapseSelected()
		return nil, true
	case "g", "home":
		m.cursorHome()
		return nil, true
	case "G", "end":
		m.cursorEnd()
		return nil, true
	case "enter":
		m.activateSelected()
		return nil, true
	case "esc", "ctrl+c":
		m.Close()
		return nil, true
	}
	return nil, true // tree layer swallows every key while visible
}

// activateSelected emits a switch Command for the cursor's session when it is
// switchable (not detached). Detached rows are a no-op — the host gates them
// off and the row renders with the dimmed glyph.
func (m *Model) activateSelected() {
	n := m.selectedNode()
	if n == nil {
		return
	}
	if n.Avail == AvailDetached {
		return
	}
	m.out = Command{Type: CommandSwitch, ID: n.ID}
}

func (m *Model) expandSelected() {
	n := m.selectedNode()
	if n == nil || n.TurnCount == 0 {
		return
	}
	if m.expanded[n.ID] {
		return
	}
	m.expanded[n.ID] = true
	m.rebuildPinned(n.ID)
}

func (m *Model) collapseSelected() {
	n := m.selectedNode()
	if n == nil {
		return
	}
	if !m.expanded[n.ID] {
		return
	}
	m.expanded[n.ID] = false
	m.rebuildPinned(n.ID)
}

// moveCursor steps to the next/previous SELECTABLE (session) row, clamping at
// the ends (no wrap, per the design's tree keymap).
func (m *Model) moveCursor(delta int) {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	i := m.cursor + delta
	for i >= 0 && i < len(m.rows) {
		if m.rows[i].nodeIdx >= 0 {
			m.cursor = i
			return
		}
		i += delta
	}
	// Hit an end without finding another session row — stay put (clamp).
}

func (m *Model) cursorHome() {
	for i := range m.rows {
		if m.rows[i].nodeIdx >= 0 {
			m.cursor = i
			return
		}
	}
}

func (m *Model) cursorEnd() {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].nodeIdx >= 0 {
			m.cursor = i
			return
		}
	}
}

// selectID pins the cursor to the session row for id, if present.
func (m *Model) selectID(id string) {
	for i, r := range m.rows {
		if r.nodeIdx >= 0 && m.nodes[r.nodeIdx].ID == id {
			m.cursor = i
			return
		}
	}
}

// rebuildPinned rebuilds the flattened rows and re-pins the cursor to the
// same session id (the design's cursor re-pin after an expand/collapse so the
// selection doesn't jump). Falls back to a clamp when the node vanished.
func (m *Model) rebuildPinned(id string) {
	m.rebuildRows()
	before := m.cursor
	m.selectID(id)
	if m.selectedNode() == nil {
		// id no longer selectable — clamp to a valid row near where we were.
		m.cursor = before
		m.clampCursor()
	}
}

// rebuildRows flattens m.nodes (already in DFS order from the host) into
// structural rows, inserting expanded turn rows under each expanded session.
// The host hands nodes pre-ordered + pre-depthed, so this is a single pass:
// one session header row per node, plus its turn rows when expanded. Row
// text is rendered later, in View, so the selection highlight follows the
// cursor without a rebuild.
func (m *Model) rebuildRows() {
	rows := make([]row, 0, len(m.nodes)*2)
	for i, n := range m.nodes {
		rows = append(rows, row{nodeIdx: i})
		if m.expanded[n.ID] {
			for _, t := range n.Turns {
				rows = append(rows, row{nodeIdx: -1, turnText: t})
			}
		}
	}
	m.rows = rows
	m.clampCursor()
}

// clampCursor pulls the cursor onto a valid selectable row after a rebuild.
func (m *Model) clampCursor() {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	// If the clamped row isn't selectable, walk to the nearest one.
	if m.rows[m.cursor].nodeIdx < 0 {
		for i := m.cursor; i >= 0; i-- {
			if m.rows[i].nodeIdx >= 0 {
				m.cursor = i
				return
			}
		}
		for i := m.cursor; i < len(m.rows); i++ {
			if m.rows[i].nodeIdx >= 0 {
				m.cursor = i
				return
			}
		}
	}
}
