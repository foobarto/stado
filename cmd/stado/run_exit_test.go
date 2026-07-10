package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestRunLoopUsesExitCode2OnlyForTypedLimits(t *testing.T) {
	for _, err := range []error{
		runtime.ErrTokenCapExceeded,
		runtime.ErrMaxTurnsExceeded,
		runtime.ErrVerifyExhausted,
	} {
		if !runLoopUsesExitCode2(err) {
			t.Fatalf("typed limit %v did not map to exit 2", err)
		}
	}
	if runLoopUsesExitCode2(errors.New("runtime: buffered verification candidate exceeded event budget")) {
		t.Fatal("untyped internal error mapped to exit 2")
	}
}

func TestRunLandlockPolicyIncludesExactAuditPaths(t *testing.T) {
	sess := &stadogit.Session{
		WorktreePath: "/state/worktrees/session-1",
		Sidecar:      &stadogit.Sidecar{Path: "/data/sessions/repo.git"},
	}
	policy := runLandlockPolicy("/work/repo", sess)
	for _, want := range []string{"/work/repo", "/tmp", sess.WorktreePath, sess.Sidecar.Path} {
		if !slices.Contains(policy.FSWrite, want) {
			t.Fatalf("landlock write paths=%v, missing %q", policy.FSWrite, want)
		}
	}
}
