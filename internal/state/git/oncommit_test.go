package git

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestOnCommit_FiresForTraceAndTree(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "oc", plumbing.ZeroHash)

	var got []CommitEvent
	sess.OnCommit = func(ev CommitEvent) { got = append(got, ev) }

	_, err := sess.CommitToTrace(CommitMeta{Tool: "grep", ShortArg: "foo"})
	if err != nil {
		t.Fatal(err)
	}

	// Make a tree commit too.
	tree, _ := sess.writeEmptyTree()
	if _, err := sess.CommitToTree(tree, CommitMeta{Tool: "write"}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 OnCommit events, got %d", len(got))
	}
	if got[0].Meta.Tool != "grep" {
		t.Errorf("first event tool = %q", got[0].Meta.Tool)
	}
	if !isTraceRef(got[0].Ref) {
		t.Errorf("first event ref should be trace: %q", got[0].Ref)
	}
	if got[1].Meta.Tool != "write" {
		t.Errorf("second event tool = %q", got[1].Meta.Tool)
	}
	if !isTreeRef(got[1].Ref) {
		t.Errorf("second event ref should be tree: %q", got[1].Ref)
	}
}

func TestOnCommit_NilIsSafe(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "oc-nil", plumbing.ZeroHash)
	// OnCommit left nil; commit should succeed without panic.
	if _, err := sess.CommitToTrace(CommitMeta{Tool: "noop"}); err != nil {
		t.Errorf("commit with nil OnCommit: %v", err)
	}
}

func isTraceRef(s string) bool {
	for _, suffix := range []string{"/trace"} {
		if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

func isTreeRef(s string) bool {
	for _, suffix := range []string{"/tree"} {
		if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}
