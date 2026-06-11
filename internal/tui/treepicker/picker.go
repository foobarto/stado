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
// Navigation + SWITCH ship in stages 3-4; BRANCH (stage 5) and PEEK
// (stage 6) layer on top. Turn rows are selectable so `b` over a turn forks
// the parent at that turn (CommandBranch) and Enter over a turn opens a
// read-only peek (CommandPeek); the host owns the fork/peek mechanics and
// drains the outbox via TakeAction.
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
	// Turns are the per-turn rows shown when the node is expanded. The host
	// pre-renders Text (e.g. "turn 3 · fix parser") and carries the turn
	// Number + CommitHex so a branch/peek over a turn row addresses an exact
	// commit without the picker needing a runtime handle.
	Turns []Turn
}

// Turn is one expanded turn row under a session. The picker renders Text and,
// when the row is the cursor target, addresses the branch/peek action by the
// owning session id (the Node's ID) + this turn's Number / CommitHex.
type Turn struct {
	// Number is the 1-based turn number (the N in refs/sessions/<id>/turns/N).
	Number int
	// CommitHex is the turn-boundary commit hash, hex-encoded. The host parses
	// it back into a plumbing.Hash for runtime.ForkSessionAtTurn / peek.
	CommitHex string
	// Text is the pre-rendered summary line ("turn 3 · fix parser").
	Text string
}

// CommandType enumerates the actions the picker can emit to the host.
type CommandType int

const (
	// CommandNone — no action this Update (navigation, expand/collapse).
	CommandNone CommandType = iota
	// CommandSwitch — Enter over a switchable session row. Host calls
	// switchToSession(ID) and closes the picker.
	CommandSwitch
	// CommandBranch — `b` over a turn row. Host calls
	// forkAndSwitchSessionAtTurn(ID, TurnCommit), forking the owning session
	// at that turn and switching to the child, then closes the picker.
	CommandBranch
	// CommandPeek — Enter over a turn row. Host opens a read-only peek of the
	// owning session's transcript (no mutation, no switch).
	CommandPeek
)

// Command is the picker's outbox payload. The host drains it via TakeAction
// after each handled Update.
type Command struct {
	Type CommandType
	// ID is the target session id: the switch target for CommandSwitch, or the
	// owning (parent) session for a turn-row branch/peek.
	ID string
	// TurnNumber / TurnCommit address the turn for CommandBranch / CommandPeek
	// (zero-valued for CommandSwitch).
	TurnNumber int
	TurnCommit string
	// TurnTotal is the owning session's tip turn number (its highest turn). The
	// host compares it to TurnNumber to decide whether the peek's full-
	// transcript banner ("session has more turns than N") applies. Set for
	// CommandPeek; zero otherwise.
	TurnTotal int
}

// row is one line in the flattened DFS: either a session header row or one of
// its expanded turn rows. BOTH are landable by the cursor now — a turn row
// carries its owning node index plus the turn it represents so a branch/peek
// over it addresses an exact commit. Rows carry structure, not rendered text
// — View renders each visible row fresh every frame so the selection
// highlight tracks the cursor without a rebuild (mirrors the
// taskpicker/palette render-on-View idiom).
type row struct {
	// nodeIdx indexes Model.nodes. For a session header row that's the row's
	// own node; for a turn row it's the OWNING session (so branch/peek know
	// the parent id). Always ≥ 0 now — turn rows are landable too.
	nodeIdx int
	// isTurn distinguishes a turn row from its session header row.
	isTurn bool
	// turn is the turn this row represents (turn rows only).
	turn Turn
}

// Model is the treepicker modal. It owns the forest's flattened rows, the
// cursor, per-session expansion state, an outbox Command, a transient notice
// (e.g. "press b on a turn"), and a read-only peek overlay layered on top.
type Model struct {
	Visible bool
	Width   int
	Height  int

	nodes    []Node
	rows     []row
	cursor   int             // index into rows; a session header or a turn row
	expanded map[string]bool // session id -> expanded
	out      Command
	// notice is a transient one-line hint rendered in the footer (cleared on
	// the next handled key) — e.g. "press b on a turn to branch".
	notice string
	// total / truncated mirror Forest.Total / Forest.Truncated so the footer
	// can surface "N sessions" and a "(capped — more exist)" warning when the
	// forest hit MaxForestSessions.
	total     int
	truncated bool
	// peek is the read-only transcript overlay layered over the tree. nil when
	// not peeking. The host fills it via OpenPeek and it owns its own scroll.
	peek *Peek
}

// New returns an empty, hidden picker.
func New() *Model { return &Model{expanded: map[string]bool{}} }

// SetStats records the forest's session count + truncation flag so the footer
// can surface them (the design's stage-7 cap-footer). Call before Open.
func (m *Model) SetStats(total int, truncated bool) {
	m.total = total
	m.truncated = truncated
}

// Open shows the picker over the supplied flattened forest. currentID, when
// present, is selected; expandIDs (the current session's full lineage —
// current + every ancestor up to its root, computed by the host) are
// auto-expanded so the whole working lineage's turns are visible on open, not
// just the current node.
func (m *Model) Open(nodes []Node, currentID string, expandIDs ...string) {
	m.Visible = true
	m.nodes = append([]Node(nil), nodes...)
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	// Auto-expand the whole current lineage (stage-7 polish — was just the
	// current node) so an operator forked several levels deep sees every turn
	// on the path to a root without hand-expanding each ancestor.
	if currentID != "" {
		m.expanded[currentID] = true
	}
	for _, id := range expandIDs {
		if id != "" {
			m.expanded[id] = true
		}
	}
	m.rebuildRows()
	m.cursor = 0
	if currentID != "" {
		m.selectID(currentID)
	}
}

// Close hides the picker (and any open peek). Expansion state is retained so
// re-opening lands in the same shape.
func (m *Model) Close() {
	m.peek = nil
	m.Visible = false
}

// Peeking reports whether the read-only transcript overlay is open. The host's
// layered Esc/Ctrl+C routing checks this so the first close drops the peek and
// the second closes the tree.
func (m *Model) Peeking() bool { return m.peek != nil }

// ClosePeek dismisses just the peek overlay, leaving the tree open. No-op when
// not peeking.
func (m *Model) ClosePeek() { m.peek = nil }

// OpenPeek layers a read-only transcript overlay over the tree. The host
// builds the Peek (rendered transcript lines + honest label/banner) from a
// deterministic, read-only LoadConversation — it NEVER opens or mutates the
// session. A nil peek is ignored.
func (m *Model) OpenPeek(p *Peek) {
	if p == nil {
		return
	}
	m.peek = p
}

// TakeAction returns the pending Command and clears the outbox. The host
// calls this after a handled Update to act on a switch.
func (m *Model) TakeAction() Command {
	c := m.out
	m.out = Command{}
	return c
}

// SelectedID returns the session id under the cursor — the owning session for
// a turn row — or "" when there is no selectable row.
func (m *Model) SelectedID() string {
	if n := m.selectedNode(); n != nil {
		return n.ID
	}
	return ""
}

// SelectedIsTurn reports whether the cursor is on a (selectable) turn row.
func (m *Model) SelectedIsTurn() bool {
	r := m.selectedRow()
	return r != nil && r.isTurn
}

// selectedRow returns the cursor's row, or nil when there is none.
func (m *Model) selectedRow() *row {
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
	return &r
}

// selectedNode returns the session the cursor's row belongs to (the owning
// session for a turn row).
func (m *Model) selectedNode() *Node {
	r := m.selectedRow()
	if r == nil {
		return nil
	}
	return &m.nodes[r.nodeIdx]
}

// Update consumes a keypress while Visible. Returns (cmd, handled); handled
// means the host must short-circuit so the keystroke doesn't leak to the
// textarea beneath. The tree-layer keymap:
//
//	↑/k ↓/j   move (clamp, no wrap; lands on session AND turn rows)
//	→/l       expand the cursor's session
//	←/h       collapse the cursor's session
//	g / G     home / end
//	enter     session row → switch (if switchable); turn row → peek
//	b         turn row → branch (fork the owner at that turn); session → notice
//	esc/ctrl+c close (peek-first when peeking — see Peeking)
//
// When a peek overlay is open it consumes keys first: scroll/navigation keys
// go to the peek, and the first Esc/Ctrl+C closes the peek (not the tree).
func (m *Model) Update(msg tea.Msg) (tea.Cmd, bool) {
	if !m.Visible {
		return nil, false
	}
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	// Peek-first: while peeking, the overlay owns the keymap and the first
	// Esc/Ctrl+C closes JUST the peek (layered close — the design's
	// Peeking()/ClosePeek() flag).
	if m.peek != nil {
		switch km.String() {
		case "esc", "ctrl+c":
			m.ClosePeek()
			return nil, true
		case "b":
			// Branch-here from inside the peek — emit a branch for the peeked
			// turn, close the peek + tree (the host forks + switches).
			m.out = Command{
				Type:       CommandBranch,
				ID:         m.peek.SessionID,
				TurnNumber: m.peek.Turn,
				TurnCommit: m.peek.Commit,
			}
			return nil, true
		default:
			m.peek.Update(km)
			return nil, true
		}
	}

	m.notice = "" // any handled key clears a stale notice
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
	case "b":
		m.branchSelected()
		return nil, true
	case "esc", "ctrl+c":
		m.Close()
		return nil, true
	}
	return nil, true // tree layer swallows every key while visible
}

// activateSelected resolves Enter: a session row emits a switch Command (when
// switchable — detached rows are a no-op the host gates off with the dimmed
// glyph); a turn row emits a peek Command for the owning session at that turn.
func (m *Model) activateSelected() {
	r := m.selectedRow()
	if r == nil {
		return
	}
	owner := &m.nodes[r.nodeIdx]
	if r.isTurn {
		if owner.Avail == AvailDetached {
			m.notice = "detached session — no worktree to peek"
			return
		}
		tip := r.turn.Number
		if n := len(owner.Turns); n > 0 {
			tip = owner.Turns[n-1].Number // turns are ascending — last is the tip
		}
		m.out = Command{
			Type:       CommandPeek,
			ID:         owner.ID,
			TurnNumber: r.turn.Number,
			TurnCommit: r.turn.CommitHex,
			TurnTotal:  tip,
		}
		return
	}
	if owner.Avail == AvailDetached {
		return
	}
	m.out = Command{Type: CommandSwitch, ID: owner.ID}
}

// branchSelected resolves `b`: a turn row emits a branch Command (fork the
// owning session at that turn); a session row sets a notice telling the
// operator to land on a turn first (design Q4 — don't duplicate the session
// picker's fork-from-tip here).
func (m *Model) branchSelected() {
	r := m.selectedRow()
	if r == nil {
		return
	}
	if !r.isTurn {
		m.notice = "press b on a turn row to branch (→ expands a session)"
		return
	}
	m.out = Command{
		Type:       CommandBranch,
		ID:         m.nodes[r.nodeIdx].ID,
		TurnNumber: r.turn.Number,
		TurnCommit: r.turn.CommitHex,
	}
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

// moveCursor steps to the next/previous row (session headers AND expanded turn
// rows are all landable), clamping at the ends (no wrap, per the design's tree
// keymap).
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

// selectID pins the cursor to the session HEADER row for id, if present. (Turn
// rows share the owner's nodeIdx, so the !isTurn guard keeps re-pins landing on
// the header rather than the first turn underneath it.)
func (m *Model) selectID(id string) {
	for i, r := range m.rows {
		if r.nodeIdx >= 0 && !r.isTurn && m.nodes[r.nodeIdx].ID == id {
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
				rows = append(rows, row{nodeIdx: i, isTurn: true, turn: t})
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
