package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/treepicker"
)

// openTreePicker builds the session forest for the current repo and opens the
// treepicker over a flattened (DFS-ordered) view of it. The forest is built
// synchronously via runtime.BuildForest — a single References() pass, bucketed,
// ~1–1.5s at the ~159-session scale (the design's eager-build budget). The
// background escape-hatch above a dormant threshold is a stage-7 polish item.
func (m *Model) openTreePicker() error {
	if m.session == nil || m.session.Sidecar == nil {
		return fmt.Errorf("session tree: no live session")
	}
	worktreeRoot := filepath.Dir(m.session.WorktreePath)
	forest, err := runtime.BuildForest(m.session.Sidecar, worktreeRoot, m.session.ID)
	if err != nil {
		return fmt.Errorf("session tree: %w", err)
	}
	nodes := flattenForest(forest)
	m.treePick.SetStats(forest.Total, forest.Truncated)
	m.treePick.Open(nodes, m.session.ID, currentLineageIDs(forest, m.session.ID)...)
	return nil
}

// currentLineageIDs returns the current session's full ancestry — itself plus
// every ParentID up to a root — so the picker auto-expands the whole working
// lineage on open (stage-7 polish), not just the current node. A cycle guard
// keeps a corrupt ParentID loop from spinning.
func currentLineageIDs(f *runtime.Forest, currentID string) []string {
	if f == nil || currentID == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for cur := currentID; cur != ""; {
		if seen[cur] {
			break
		}
		seen[cur] = true
		out = append(out, cur)
		node, ok := f.Sessions[cur]
		if !ok {
			break
		}
		cur = node.ParentID
	}
	return out
}

// openTreePeek layers a READ-ONLY transcript overlay over the tree for the
// turn the cursor is on. It is strictly non-mutating: it derives the target
// session's worktree path deterministically (cfg.WorktreeDir()/<id>), reads
// the conversation with runtime.LoadConversation (no OpenSessionByID, no ref
// write), renders it via the same msgsToBlocks path the live view uses, and
// hands the rendered lines + an HONEST label to the picker. m.session,
// m.blocks, and every session ref are untouched.
//
// The label is deliberately honest: peek shows the WHOLE conversation on disk,
// not a point-in-time snapshot at turn N (turns are git tags;
// conversation.jsonl has no per-message turn field — exact slicing needs a
// data-model change that's out of v1 scope). When the session has more turns
// than the peeked one, a muted banner says so.
//
// turnTotal is the owning session's tip turn (from the picker). A peeked turn
// below the tip means the transcript shows content AFTER turn N → banner.
func (m *Model) openTreePeek(id string, turn, turnTotal int, atCommit string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session peek: no session selected")
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	// Deterministic, read-only worktree path — NEVER OpenSessionByID (which
	// would activate/mutate). Avail-gate: a session with no worktree on disk
	// (detached) has nothing to peek.
	wt := filepath.Join(cfg.WorktreeDir(), id)
	if _, err := os.Stat(wt); err != nil {
		return fmt.Errorf("session peek: no worktree for %s (detached)", shortSessionID(id))
	}
	msgs, err := runtime.LoadConversation(wt)
	if err != nil {
		return fmt.Errorf("session peek: %w", err)
	}

	// Render the conversation to plain lines through the SAME block path the
	// live conversation uses — purely local, nothing assigned to m.blocks.
	width := m.vp.Width() - 2
	if width < 20 {
		width = 60
	}
	var lines []string
	for _, blk := range msgsToBlocks(msgs) {
		out, _ := m.renderBlock(blk, width)
		out = strings.TrimRight(out, "\n")
		lines = append(lines, strings.Split(out, "\n")...)
	}

	label := fmt.Sprintf("transcript — session %s @ turns/%d (read-only · full conversation, not a point-in-time snapshot)",
		shortSessionID(id), turn)
	banner := ""
	if turnTotal > turn {
		banner = fmt.Sprintf("⚠ showing the FULL transcript — this session has %d turns; content after turn %d is also below.", turnTotal, turn)
	}
	m.treePick.OpenPeek(treepicker.NewPeek(id, turn, atCommit, label, banner, lines))
	return nil
}

// flattenForest walks the forest depth-first (roots in their sorted order,
// then each node's children sorted the same way) into the flat []Node the
// treepicker renders. The treepicker holds only strings + scroll state, so all
// the per-node rendering inputs (label, meta, depth, availability, fork tag)
// are computed here against the rich runtime types.
func flattenForest(f *runtime.Forest) []treepicker.Node {
	if f == nil {
		return nil
	}
	// Build the children adjacency from ParentID. Roots are already sorted by
	// BuildForest; sort each child slice by the same key so the DFS order is
	// deterministic and matches the forest's own sort.
	children := map[string][]*runtime.SessionNode{}
	for _, n := range f.Sessions {
		if n.ParentID != "" {
			children[n.ParentID] = append(children[n.ParentID], n)
		}
	}
	for pid := range children {
		sortSessionNodes(children[pid])
	}

	out := make([]treepicker.Node, 0, len(f.Sessions))
	var walk func(n *runtime.SessionNode)
	seen := map[string]bool{}
	walk = func(n *runtime.SessionNode) {
		if n == nil || seen[n.ID] {
			return // cycle / re-entry guard
		}
		seen[n.ID] = true
		out = append(out, toTreeNode(n))
		for _, c := range children[n.ID] {
			walk(c)
		}
	}
	for _, r := range f.Roots {
		walk(r)
	}
	return out
}

// sortSessionNodes mirrors BuildForest's sibling order well enough for a
// stable DFS: most-recently-active first, then by id. (Current-lineage pinning
// is already applied to the Roots slice by BuildForest; for child slices the
// recency order is what matters.)
func sortSessionNodes(nodes []*runtime.SessionNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.IsCurrent != b.IsCurrent {
			return a.IsCurrent
		}
		if !a.Summary.LastActive.Equal(b.Summary.LastActive) {
			return a.Summary.LastActive.After(b.Summary.LastActive)
		}
		return a.ID < b.ID
	})
}

// toTreeNode maps one runtime.SessionNode onto the picker's plain Node,
// computing the right-aligned meta column the same way the session manager
// does (status + turns + msgs + last-active).
func toTreeNode(n *runtime.SessionNode) treepicker.Node {
	label := shortSessionID(n.ID)
	if n.Description != "" {
		label = n.Description
	}
	meta := fmt.Sprintf("%s  turns=%d msgs=%d", n.Summary.Status, n.Summary.Turns, n.Summary.Msgs)
	if !n.Summary.LastActive.IsZero() {
		meta = n.Summary.LastActive.UTC().Format("01-02 15:04") + "  " + meta
	}

	turns := make([]treepicker.Turn, 0, len(n.Turns))
	for _, t := range n.Turns {
		summary := t.Entry.Summary
		if summary == "" {
			summary = "(no summary)"
		}
		turns = append(turns, treepicker.Turn{
			Number:       t.Entry.Turn,
			CommitHex:    t.Entry.Commit.String(),
			Text:         fmt.Sprintf("turn %d · %s", t.Entry.Turn, summary),
			MutatedCount: t.MutatedCount,
			DeniedCount:  t.DeniedCount,
		})
	}

	return treepicker.Node{
		ID:           n.ID,
		Label:        label,
		Meta:         meta,
		Depth:        n.Depth,
		Avail:        treepickerAvail(n.Avail),
		IsCurrent:    n.IsCurrent,
		ParentTurn:   n.ParentTurn,
		HasParent:    n.ParentID != "",
		Orphan:       n.Orphan,
		TurnCount:    len(n.Turns),
		Turns:        turns,
		MutatedCount: n.MutatedTotal,
		DeniedCount:  n.DeniedTotal,
	}
}

// treepickerAvail maps runtime.Availability onto the picker's local enum so the
// treepicker package stays free of a runtime import.
func treepickerAvail(a runtime.Availability) treepicker.Avail {
	switch a {
	case runtime.AvailLive:
		return treepicker.AvailLive
	case runtime.AvailIdle:
		return treepicker.AvailIdle
	default:
		return treepicker.AvailDetached
	}
}
