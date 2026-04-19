package git

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// TestListCompactions_NewestFirst: ListCompactions walks tree ref
// from HEAD and returns compaction commits in newest-first order.
// Non-compaction commits (ordinary tool calls / seed commits) are
// skipped so the list is dense with markers only.
func TestListCompactions_NewestFirst(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-markers", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	// Seed with an ordinary tree commit (non-compaction).
	emptyTree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	if _, err := sess.CommitToTree(emptyTree, CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	// First compaction.
	if _, _, err := sess.CommitCompaction(CompactionMeta{
		Title:      "consolidated exploration",
		Summary:    "looked at auth module",
		FromTurn:   0,
		ToTurn:     5,
		TurnsTotal: 6,
		ByAuthor:   "tester",
	}); err != nil {
		t.Fatal(err)
	}

	// Ordinary commit in between.
	if _, err := sess.CommitToTree(emptyTree, CommitMeta{Tool: "edit", Summary: "after-compaction work"}); err != nil {
		t.Fatal(err)
	}

	// Second compaction.
	if _, _, err := sess.CommitCompaction(CompactionMeta{
		Title:      "second pass",
		FromTurn:   6,
		ToTurn:     10,
		TurnsTotal: 5,
		ByAuthor:   "tester",
	}); err != nil {
		t.Fatal(err)
	}

	markers, err := sc.ListCompactions(sess.ID)
	if err != nil {
		t.Fatalf("ListCompactions: %v", err)
	}
	if len(markers) != 2 {
		t.Fatalf("got %d markers, want 2: %+v", len(markers), markers)
	}
	// Newest first: second pass should appear before consolidated exploration.
	if markers[0].Title != "second pass" {
		t.Errorf("markers[0].Title = %q, want \"second pass\"", markers[0].Title)
	}
	if markers[1].Title != "consolidated exploration" {
		t.Errorf("markers[1].Title = %q, want \"consolidated exploration\"", markers[1].Title)
	}

	// Trailer parsing — pick one and check every field.
	m := markers[1]
	if m.FromTurn != 0 || m.ToTurn != 5 || m.TurnsTotal != 6 {
		t.Errorf("turn trailers: from=%d to=%d total=%d (want 0/5/6)",
			m.FromTurn, m.ToTurn, m.TurnsTotal)
	}
	if m.By != "tester" {
		t.Errorf("By = %q, want \"tester\"", m.By)
	}
	if !strings.HasPrefix(m.At, "20") {
		t.Errorf("At trailer looks non-ISO8601: %q", m.At)
	}
}

// TestListCompactions_NoMarkers returns empty slice + no error for a
// session that hasn't compacted. Callers rely on this for clean
// "no compaction events" output.
func TestListCompactions_NoMarkers(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-none", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	if _, err := sess.CommitToTree(emptyTree, CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	markers, err := sc.ListCompactions(sess.ID)
	if err != nil {
		t.Fatalf("ListCompactions: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers, got %d", len(markers))
	}
}

// TestParseCompactionCommit_RejectsNonCompaction — the guard against
// mislabelling ordinary tool-call commits. A commit whose subject
// doesn't start with "Compaction: " is not a marker, full stop.
func TestParseCompactionCommit_RejectsNonCompaction(t *testing.T) {
	_, ok := parseCompactionCommit("grep(auth): searched\n\nTool: grep\nTurn: 3\n")
	if ok {
		t.Error("expected false for non-compaction commit")
	}
}
