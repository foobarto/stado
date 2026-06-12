package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestResolveRefClassified verifies the classifier that the display/aggregate
// commands now use to tell a ref that legitimately doesn't exist yet (benign,
// nil error) apart from a real git-storage failure (non-nil error). Before this
// the sites collapsed both into "absent", so a corrupt sidecar surfaced as
// "(unset)" / a silently skipped session instead of an error.
func TestResolveRefClassified(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	// Not-found: a ref that was never created -> ok=false, nil error (benign).
	if _, ok, err := resolveRefClassified(sc, stadogit.TraceRef("never-created")); ok || err != nil {
		t.Fatalf("missing ref: got ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// Found: a session with a trace commit -> non-zero hash, ok=true, nil error.
	const id = "classify-found"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if h, ok, err := resolveRefClassified(sc, stadogit.TraceRef(id)); !ok || err != nil || h.IsZero() {
		t.Fatalf("existing ref: got hash=%s ok=%v err=%v, want a non-zero hash, ok=true, nil err", h, ok, err)
	}

	// Real storage error: corrupt packed-refs (go-git parses it during ref
	// resolution) so a lookup with no matching loose ref fails -> ok=false,
	// non-nil error that is NOT ErrReferenceNotFound.
	if err := os.WriteFile(filepath.Join(sc.Path, "packed-refs"),
		[]byte("garbage not a ref line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok, err := resolveRefClassified(sc, stadogit.TraceRef("missing-after-corrupt"))
	if err == nil {
		t.Fatalf("corrupt packed-refs: expected a real storage error, got ok=%v nil err", ok)
	}
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("real storage error misclassified as not-found: %v", err)
	}
}
