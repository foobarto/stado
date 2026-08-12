package artifacts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
)

func fixture(t *testing.T) (*Service, *authority.Issuer, *wal.Store) {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	issuer, consumer := authority.New(store)
	return NewService(store, consumer), issuer, store
}

func TestLessonValidationAndSessionDescendantScope(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	if _, err := svc.Create(ctx, Artifact{Kind: KindLesson, Scope: ScopeGlobal, Summary: "bad"}, "alice", "agent", "bad"); err == nil {
		t.Fatal("lesson without trigger accepted")
	}
	item, err := svc.Create(ctx, Artifact{Kind: KindLesson, Scope: ScopeSession, Binding: ScopeBinding{AnchorSessionID: "parent"}, Summary: "Retry JSON tool calls", Trigger: "tool rejects malformed JSON", Tags: []string{" Tool:JSON ", "tool:json"}}, "alice", "agent", "create")
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Tags) != 1 || item.Tags[0] != "tool:json" {
		t.Fatalf("tags=%v", item.Tags)
	}
	for name, q := range map[string]QueryContext{
		"anchor":     {Principal: "alice", SessionID: "parent"},
		"descendant": {Principal: "alice", SessionID: "child", AncestorSessionIDs: []string{"parent"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.Query(Query{Context: q})
			if err != nil || len(got) != 1 {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
	for name, q := range map[string]QueryContext{
		"sibling":   {Principal: "alice", SessionID: "sibling", AncestorSessionIDs: []string{"root"}},
		"principal": {Principal: "mallory", SessionID: "parent"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.Query(Query{Context: q})
			if err != nil || len(got) != 0 {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestActivationNeedsExactOneUseOperatorGrant(t *testing.T) {
	svc, issuer, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	item, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeRepo, Binding: ScopeBinding{CanonicalRepoID: "repo-1"}, Summary: "Use app schema v2", Content: "Call with field x"}, "alice", "agent", "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetAuthority(ctx, item.ID, 1, AuthorityActive, "", "alice", "agent", "activate-no-grant"); !errors.Is(err, ErrOperatorGrant) {
		t.Fatalf("got %v", err)
	}
	action, _ := ActivationAction(item, "alice")
	wrong := action
	wrong.Version++
	badGrant, err := issuer.Issue(ctx, wrong, "operator:alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetAuthority(ctx, item.ID, 1, AuthorityActive, badGrant.ID, "alice", "agent", "activate-bad"); !errors.Is(err, authority.ErrGrantMismatch) {
		t.Fatalf("got %v", err)
	}
	grant, err := issuer.Issue(ctx, action, "operator:alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	active, err := svc.SetAuthority(ctx, item.ID, 1, AuthorityActive, grant.ID, "alice", "broker", "activate")
	if err != nil || active.Authority != AuthorityActive || active.Version != 2 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	other, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeRepo, Binding: ScopeBinding{CanonicalRepoID: "repo-1"}, Summary: "Other"}, "alice", "agent", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetAuthority(ctx, other.ID, 1, AuthorityActive, grant.ID, "alice", "broker", "reuse"); !errors.Is(err, authority.ErrGrantMismatch) && !errors.Is(err, authority.ErrGrantConsumed) {
		t.Fatalf("got %v", err)
	}
}

func TestEditCASAndDeletedTombstone(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	item, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeGlobal, Summary: "one"}, "alice", "agent", "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Edit(ctx, item.ID, 99, Artifact{Kind: KindMemory, Summary: "two"}, "alice", "agent", "stale"); !errors.Is(err, ErrVersion) {
		t.Fatalf("got %v", err)
	}
	item, err = svc.SetAuthority(ctx, item.ID, 1, AuthorityDeleted, "", "alice", "operator", "delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Edit(ctx, item.ID, item.Version, Artifact{Kind: KindMemory, Summary: "resurrect"}, "alice", "agent", "resurrect"); err == nil {
		t.Fatal("deleted item resurrected")
	}
}
