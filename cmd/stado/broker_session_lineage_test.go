package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/broker"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestBrokerSessionLineageVerifierProvesExactDirectParentTurn(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessionsDir := t.TempDir()
	worktreeDir := t.TempDir()
	repoID, err := stadogit.RepoID(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := stadogit.OpenOrInitSidecar(filepath.Join(sessionsDir, repoID+".git"), repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := stadogit.CreateSession(sidecar, worktreeDir, "logical-source", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.NextTurn(); err != nil {
		t.Fatal(err)
	}
	forkPoint, err := sidecar.ResolveRef(stadogit.TurnTagRef(parent.ID, 1))
	if err != nil {
		t.Fatal(err)
	}
	child, err := stadogit.CreateSession(sidecar, worktreeDir, "logical-child", forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	verifier := brokerSessionLineageVerifier{sessionsDir: sessionsDir, worktreeDir: worktreeDir}
	check := broker.SessionLineageCheck{
		SourceCWD: repoRoot, SourceSubject: parent.ID, ChildSubject: child.ID,
		SourceTurnRef: "refs/sessions/" + parent.ID + "/turns/1",
	}
	if err := verifier.VerifyDirectChild(context.Background(), check); err != nil {
		t.Fatalf("direct child rejected: %v", err)
	}
	check.SourceTurnRef = "refs/sessions/" + parent.ID + "/turns/2"
	if err := verifier.VerifyDirectChild(context.Background(), check); err == nil {
		t.Fatal("wrong source turn was accepted")
	}

	if err := child.NextTurn(); err != nil {
		t.Fatal(err)
	}
	childTurn, err := sidecar.ResolveRef(stadogit.TurnTagRef(child.ID, 1))
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := stadogit.CreateSession(sidecar, worktreeDir, "logical-grandchild", childTurn)
	if err != nil {
		t.Fatal(err)
	}
	check.ChildSubject = grandchild.ID
	check.SourceTurnRef = "refs/sessions/" + parent.ID + "/turns/1"
	if err := verifier.VerifyDirectChild(context.Background(), check); err == nil {
		t.Fatal("descendant was accepted as a direct child")
	}
}
