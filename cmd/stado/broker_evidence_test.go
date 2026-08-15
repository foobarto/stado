package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

func TestSessionEvidenceLineageIsBoundedAndNearestFirst(t *testing.T) {
	ancestors := make([]string, maxSessionEvidenceLineage+20)
	for i := range ancestors {
		ancestors[i] = fmt.Sprintf("ancestor-%03d", i)
	}
	lineage := boundedSessionEvidenceLineage("current", ancestors)
	if len(lineage) != maxSessionEvidenceLineage || lineage[0] != "current" || lineage[len(lineage)-1] != ancestors[maxSessionEvidenceLineage-2] {
		t.Fatalf("bounded lineage = %v", lineage)
	}
}

func sessionEvidenceFixture(t *testing.T) (*config.Config, broker.EvidenceSessionScope, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	rootSession, err := runtime.NewSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	foreignSession, err := runtime.NewSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.AppendMessage(rootSession.WorktreePath, agent.Text(agent.RoleUser, "immutable evidence needle")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.AppendMessage(foreignSession.WorktreePath, agent.Text(agent.RoleUser, "foreign secret needle")); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(repo)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, broker.EvidenceSessionScope{CanonicalRepo: repoID, RootSessionID: rootSession.ID}, rootSession.WorktreePath, foreignSession.ID
}

func TestSessionEvidenceEnumeratesOnlyAuthorizedLineage(t *testing.T) {
	cfg, scope, _, foreignID := sessionEvidenceFixture(t)
	source := brokerSessionEvidenceSource{cfg: cfg}
	items, err := source.Catalog(context.Background(), scope, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Ref.ID != scope.RootSessionID {
		t.Fatalf("catalog = %+v, want only authenticated root", items)
	}
	foreign := items[0].Ref
	foreign.ID = foreignID
	if _, err := source.Open(context.Background(), scope, foreign, 32<<10); err == nil {
		t.Fatal("foreign session reference was readable")
	}
	results, err := source.Search(context.Background(), scope, "foreign secret", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("foreign session was searchable: %+v", results)
	}
}

func TestSessionEvidenceRangeSurvivesAppendCompactionAndRestart(t *testing.T) {
	cfg, scope, worktree, _ := sessionEvidenceFixture(t)
	source := brokerSessionEvidenceSource{cfg: cfg}
	results, err := source.Search(context.Background(), scope, "evidence needle", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("search: items=%+v err=%v", results, err)
	}
	ref := results[0].Ref
	opened, err := source.Open(context.Background(), scope, ref, 32<<10)
	if err != nil || !strings.Contains(opened.Body, "immutable evidence needle") {
		t.Fatalf("initial open: %+v err=%v", opened, err)
	}
	if err := runtime.AppendMessage(worktree, agent.Text(agent.RoleAssistant, "later answer")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.AppendCompaction(worktree, runtime.ConversationCompaction{
		Summary: "new compacted view", FromTurn: 1, ToTurn: 1, TurnsTotal: 1, By: "test",
	}); err != nil {
		t.Fatal(err)
	}

	// A new source instance represents broker restart. The immutable byte range
	// and digest remain valid as the append-only conversation advances.
	restarted := brokerSessionEvidenceSource{cfg: cfg}
	again, err := restarted.Open(context.Background(), scope, ref, 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	if again.Ref != ref || again.Body != opened.Body {
		t.Fatalf("range changed across append/restart: before=%+v after=%+v", opened, again)
	}

	fabricated := ref
	fabricated.Digest = digestEvidence([]byte("fabricated"))
	if _, err := restarted.Open(context.Background(), scope, fabricated, 32<<10); err == nil {
		t.Fatal("fabricated session digest was accepted")
	}
}
