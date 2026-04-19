package git

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestCommitCompaction_WritesOnBothRefs is the dual-ref spec from
// DESIGN §"Compaction": tree ref gets a new commit whose tree hash
// matches its parent's (filesystem unchanged), and trace ref gets a
// parallel empty-tree commit. Both carry the same summary payload so
// downstream tooling walking either ref sees the event.
func TestCommitCompaction_WritesOnBothRefs(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-compact", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	// Seed the session with a couple of regular tool-call commits on
	// both refs so there's real content for compaction to summarise.
	if _, err := sess.CommitToTrace(CommitMeta{Tool: "grep", Summary: "t1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(CommitMeta{Tool: "read", Summary: "t2"}); err != nil {
		t.Fatal(err)
	}
	// First tree commit — empty tree suffices for the test since we
	// only care about commit graph structure.
	emptyTree, _ := sess.writeEmptyTree()
	if _, err := sess.commitOnRef(TreeRef(sess.ID), emptyTree, CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	// Snapshot the pre-compaction heads.
	treeBefore, err := sess.TreeHead()
	if err != nil {
		t.Fatal(err)
	}
	traceBefore, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}

	// Run compaction.
	treeSHA, traceSHA, err := sess.CommitCompaction(CompactionMeta{
		Title:      "consolidated early exploration",
		Summary:    "Looked at auth module, identified rewrite scope.",
		FromTurn:   0,
		ToTurn:     2,
		TurnsTotal: 3,
		ByAuthor:   "stado-test",
	})
	if err != nil {
		t.Fatalf("CommitCompaction: %v", err)
	}
	if treeSHA == plumbing.ZeroHash || traceSHA == plumbing.ZeroHash {
		t.Fatalf("expected non-zero SHAs, got tree=%s trace=%s", treeSHA, traceSHA)
	}

	// Tree-ref invariant: new commit's TreeHash == parent's TreeHash.
	// `git checkout tree~1 -- …` must restore the pre-compaction
	// filesystem state exactly (same tree object → same files).
	treeCommit, err := object.GetCommit(sc.repo.Storer, treeSHA)
	if err != nil {
		t.Fatal(err)
	}
	parentCommit, err := object.GetCommit(sc.repo.Storer, treeBefore)
	if err != nil {
		t.Fatal(err)
	}
	if treeCommit.TreeHash != parentCommit.TreeHash {
		t.Errorf("compaction tree commit changed the tree hash: got %s, want %s (parent)",
			treeCommit.TreeHash, parentCommit.TreeHash)
	}
	if len(treeCommit.ParentHashes) != 1 || treeCommit.ParentHashes[0] != treeBefore {
		t.Errorf("tree compaction parent wrong: %v (want [%s])", treeCommit.ParentHashes, treeBefore)
	}

	// Trace-ref invariant: new commit parents the previous trace head.
	traceCommit, err := object.GetCommit(sc.repo.Storer, traceSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(traceCommit.ParentHashes) != 1 || traceCommit.ParentHashes[0] != traceBefore {
		t.Errorf("trace compaction parent wrong: %v (want [%s])", traceCommit.ParentHashes, traceBefore)
	}

	// Both commits share a message body with the summary + turn-range
	// trailers — downstream tooling keys off those.
	for _, c := range []*object.Commit{treeCommit, traceCommit} {
		if !strings.Contains(c.Message, "Compaction: consolidated early exploration") {
			t.Errorf("message missing subject: %q", c.Message)
		}
		if !strings.Contains(c.Message, "auth module") {
			t.Errorf("message missing summary body: %q", c.Message)
		}
		if !strings.Contains(c.Message, "Compaction-From-Turn: 0") ||
			!strings.Contains(c.Message, "Compaction-To-Turn: 2") {
			t.Errorf("message missing turn-range trailers: %q", c.Message)
		}
		if !strings.Contains(c.Message, "Compaction-By: stado-test") {
			t.Errorf("message missing author trailer: %q", c.Message)
		}
	}
}

// TestCommitCompaction_EmptySessionErrors — compaction on a session
// with no tree-ref HEAD can't produce a meaningful parent tree hash,
// so it errors cleanly rather than writing an orphan root commit.
func TestCommitCompaction_EmptySessionErrors(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-empty", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = sess.CommitCompaction(CompactionMeta{Title: "x", Summary: "y"})
	if err == nil {
		t.Fatal("expected error for empty-session compaction")
	}
	if !strings.Contains(err.Error(), "tree") {
		t.Errorf("error should mention tree ref, got: %v", err)
	}
}
