package tui

import (
	"fmt"
	"path/filepath"
	"sort"

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
	m.treePick.Open(nodes, m.session.ID)
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

	turns := make([]string, 0, len(n.Turns))
	for _, t := range n.Turns {
		summary := t.Entry.Summary
		if summary == "" {
			summary = "(no summary)"
		}
		turns = append(turns, fmt.Sprintf("turn %d · %s", t.Entry.Turn, summary))
	}

	return treepicker.Node{
		ID:         n.ID,
		Label:      label,
		Meta:       meta,
		Depth:      n.Depth,
		Avail:      treepickerAvail(n.Avail),
		IsCurrent:  n.IsCurrent,
		ParentTurn: n.ParentTurn,
		HasParent:  n.ParentID != "",
		Orphan:     n.Orphan,
		TurnCount:  len(n.Turns),
		Turns:      turns,
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
