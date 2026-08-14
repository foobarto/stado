package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
)

func TestProvenanceLegacyReadMigratesToStructuredWrites(t *testing.T) {
	var provenance Provenance
	if err := json.Unmarshal([]byte(`["session:parent","tool:shell"]`), &provenance); err != nil {
		t.Fatal(err)
	}
	if len(provenance.Origins) != 2 || provenance.Origins[0] != "session:parent" || provenance.CreatedBy != "" {
		t.Fatalf("provenance=%+v", provenance)
	}
	raw, err := json.Marshal(provenance)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"origins":["session:parent","tool:shell"]}` {
		t.Fatalf("new write retained legacy shape: %s", raw)
	}
}

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
	if _, err := svc.Create(ctx, testLesson(ScopeGlobal, ScopeBinding{}, "bad", ""), "alice", "agent", "bad"); err == nil {
		t.Fatal("lesson without trigger accepted")
	}
	proposal := testLesson(ScopeSession, ScopeBinding{AnchorSessionID: "parent"}, "Retry JSON tool calls", "tool rejects malformed JSON")
	proposal.Tags = []string{" Tool:JSON ", "tool:json"}
	item, err := svc.Create(ctx, proposal, "alice", "agent", "create")
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
	item, err := svc.Create(ctx, testMemory(ScopeRepo, ScopeBinding{CanonicalRepoID: "repo-1"}, "Use app schema v2", "Call with field x"), "alice", "agent", "create")
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
	other, err := svc.Create(ctx, testMemory(ScopeRepo, ScopeBinding{CanonicalRepoID: "repo-1"}, "Other", ""), "alice", "agent", "other")
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
	item, err := svc.Create(ctx, testMemory(ScopeGlobal, ScopeBinding{}, "one", ""), "alice", "agent", "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Edit(ctx, item.ID, 99, testMemory(ScopeGlobal, ScopeBinding{}, "two", ""), "alice", "agent", "stale"); !errors.Is(err, ErrVersion) {
		t.Fatalf("got %v", err)
	}
	item, err = svc.SetAuthority(ctx, item.ID, 1, AuthorityDeleted, "", "alice", "operator", "delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Edit(ctx, item.ID, item.Version, testMemory(ScopeGlobal, ScopeBinding{}, "resurrect", ""), "alice", "agent", "resurrect"); err == nil {
		t.Fatal("deleted item resurrected")
	}
}

func TestVisibleChecksExactVersionAndScopeWithoutPagination(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	first, err := svc.Create(ctx, testMemory(ScopeGlobal, ScopeBinding{}, "first", ""), "alice", "agent", "first")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if _, err := svc.Create(ctx, testMemory(ScopeGlobal, ScopeBinding{}, "newer", ""), "alice", "agent", fmt.Sprintf("newer-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if page, err := svc.Query(Query{Context: QueryContext{Principal: "alice"}, MaxItems: 50}); err != nil || len(page) != 50 {
		t.Fatalf("bounded query len=%d err=%v", len(page), err)
	} else {
		for _, item := range page {
			if item.ID == first.ID {
				t.Fatal("fixture did not place exact target beyond first result page")
			}
		}
	}
	if item, ok, err := svc.Visible(first.ID, first.Version, QueryContext{Principal: "alice"}); err != nil || !ok || item.ID != first.ID {
		t.Fatalf("exact visible item=%+v ok=%v err=%v", item, ok, err)
	}
	if exact, err := svc.Query(Query{
		Context:  QueryContext{Principal: "alice"},
		Refs:     []ArtifactRef{{ID: first.ID, Version: first.Version}},
		MaxItems: 1,
	}); err != nil || len(exact) != 1 || exact[0].ID != first.ID {
		t.Fatalf("exact query items=%+v err=%v", exact, err)
	}
	if _, ok, err := svc.Visible(first.ID, first.Version+1, QueryContext{Principal: "alice"}); err != nil || ok {
		t.Fatalf("wrong version ok=%v err=%v", ok, err)
	}
	if _, ok, err := svc.Visible(first.ID, first.Version, QueryContext{Principal: "mallory"}); err != nil || ok {
		t.Fatalf("wrong principal ok=%v err=%v", ok, err)
	}
}

func TestArtifactReferenceFieldsAreBounded(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	item := testMemory(ScopeGlobal, ScopeBinding{}, "bounded", "")
	item.EvidenceRefs = make([]string, 65)
	for i := range item.EvidenceRefs {
		item.EvidenceRefs[i] = fmt.Sprintf("ref:%d", i)
	}
	if _, err := svc.Create(context.Background(), item, "alice", "agent", "too-many-refs"); err == nil {
		t.Fatal("artifact with unbounded evidence references was accepted")
	}
}
