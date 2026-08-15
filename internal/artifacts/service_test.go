package artifacts

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	return NewServiceWithKinds(store, consumer, testKindRegistry()), issuer, store
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

func TestArtifactMutationRetriesReturnExactCommittedResults(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	proposal := testMemory(ScopeGlobal, ScopeBinding{}, "retry-safe", "body")
	created, err := svc.Create(ctx, proposal, "alice", "plugin:v1", "stable-create")
	if err != nil {
		t.Fatal(err)
	}
	retried, err := svc.Create(ctx, proposal, "alice", "plugin:v2", "stable-create")
	if err != nil || retried.ID != created.ID || retried.Version != created.Version || !retried.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("create retry=%+v original=%+v err=%v", retried, created, err)
	}
	changed := testMemory(ScopeGlobal, ScopeBinding{}, "changed", "body")
	if _, err := svc.Create(ctx, changed, "alice", "plugin:v2", "stable-create"); !errors.Is(err, wal.ErrConflict) {
		t.Fatalf("changed create retry err=%v, want WAL conflict", err)
	}

	replacement := testMemory(ScopeGlobal, ScopeBinding{}, "edited", "new body")
	edited, err := svc.Edit(ctx, created.ID, created.Version, replacement, "alice", "plugin:v1", "stable-edit")
	if err != nil {
		t.Fatal(err)
	}
	retriedEdit, err := svc.Edit(ctx, created.ID, created.Version, replacement, "alice", "plugin:v2", "stable-edit")
	if err != nil || retriedEdit.ID != edited.ID || retriedEdit.Version != edited.Version || !retriedEdit.UpdatedAt.Equal(edited.UpdatedAt) {
		t.Fatalf("edit retry=%+v original=%+v err=%v", retriedEdit, edited, err)
	}
	changedEdit := testMemory(ScopeGlobal, ScopeBinding{}, "edited differently", "new body")
	if _, err := svc.Edit(ctx, created.ID, created.Version, changedEdit, "alice", "plugin:v2", "stable-edit"); !errors.Is(err, wal.ErrConflict) {
		t.Fatalf("changed edit retry err=%v, want WAL conflict", err)
	}
	if records := store.Records(); len(records) != 2 {
		t.Fatalf("retry appended duplicate mutations: %d records", len(records))
	}
}

func TestArtifactCreateRetrySurvivesStoreRestart(t *testing.T) {
	root := t.TempDir()
	store, err := wal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, consumer := authority.New(store)
	firstService := NewServiceWithKinds(store, consumer, testKindRegistry())
	proposal := testMemory(ScopeGlobal, ScopeBinding{}, "restart-safe", "body")
	created, err := firstService.Create(context.Background(), proposal, "alice", "plugin:v1", "restart-create")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := wal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, reopenedConsumer := authority.New(reopened)
	reloadedService := NewServiceWithKinds(reopened, reopenedConsumer, testKindRegistry())
	retried, err := reloadedService.Create(context.Background(), proposal, "alice", "plugin:v2", "restart-create")
	if err != nil || retried.ID != created.ID || retried.Version != created.Version || !retried.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("restart retry=%+v original=%+v err=%v", retried, created, err)
	}
	if got := len(reopened.Records()); got != 1 {
		t.Fatalf("restart retry appended %d records, want 1", got)
	}
}

func TestArtifactQueryPageDigestFencesProjectionDrift(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	for i := 0; i < 61; i++ {
		if _, err := svc.Create(ctx, testMemory(ScopeGlobal, ScopeBinding{}, fmt.Sprintf("item-%02d", i), ""), "alice", "agent", fmt.Sprintf("create-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	query := Query{Context: QueryContext{Principal: "alice"}}
	first, err := svc.QueryPage(query, 0, 17)
	if err != nil || first.Complete || first.NextOffset != 17 || len(first.Items) != 17 || !strings.HasPrefix(first.Digest, "sha256:") {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	seen := map[string]bool{}
	digest := first.Digest
	page := first
	for {
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate paginated item %s", item.ID)
			}
			seen[item.ID] = true
		}
		if page.Complete {
			break
		}
		page, err = svc.QueryPage(query, page.NextOffset, 17)
		if err != nil || page.Digest != digest {
			t.Fatalf("next page=%+v err=%v", page, err)
		}
	}
	if len(seen) != 61 {
		t.Fatalf("paginated %d items, want 61", len(seen))
	}
	if _, err := svc.Create(ctx, testMemory(ScopeGlobal, ScopeBinding{}, "concurrent", ""), "alice", "agent", "concurrent"); err != nil {
		t.Fatal(err)
	}
	drifted, err := svc.QueryPage(query, first.NextOffset, 17)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.Digest == digest {
		t.Fatal("artifact projection digest did not change after concurrent mutation")
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
