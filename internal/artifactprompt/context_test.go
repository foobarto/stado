package artifactprompt

import (
	"context"
	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/trajectory"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildInjectsOnlyActiveAuthorizedArtifacts(t *testing.T) {
	dir := t.TempDir()
	store, err := wal.Open(filepath.Join(dir, "broker", "events"))
	if err != nil {
		t.Fatal(err)
	}
	issuer, consumer := authority.New(store)
	svc := artifacts.NewService(store, consumer)
	principal := trajectory.LocalPrincipal()
	active, err := svc.Create(context.Background(), artifacts.Artifact{Kind: artifacts.KindLesson, Scope: artifacts.ScopeSession, Binding: artifacts.ScopeBinding{AnchorSessionID: "parent"}, Summary: "Retry JSON", Content: "repair arguments", Trigger: "schema error"}, principal, "agent", "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), artifacts.Artifact{Kind: artifacts.KindMemory, Scope: artifacts.ScopeGlobal, Summary: "candidate secret phrase"}, principal, "agent", "candidate"); err != nil {
		t.Fatal(err)
	}
	action, _ := artifacts.ActivationAction(active, principal)
	grant, err := issuer.Issue(context.Background(), action, "operator", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetAuthority(context.Background(), active.ID, 1, artifacts.AuthorityActive, grant.ID, principal, "broker", "activate"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := Build(context.Background(), Options{StateDir: dir, SessionID: "child", Ancestors: []string{"parent"}, Prompt: "JSON schema", MaxItems: 8, BudgetTokens: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Retry JSON") || strings.Contains(body, "candidate secret phrase") {
		t.Fatalf("body=%q", body)
	}
}
