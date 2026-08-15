package artifacts

import (
	"context"
	"errors"
	"testing"
)

func TestRelationsNeverRevealHiddenEndpoint(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	global, err := svc.Create(ctx, testMemory(ScopeGlobal, ScopeBinding{}, "global", ""), "alice", "agent", "g")
	if err != nil {
		t.Fatal(err)
	}
	session, err := svc.Create(ctx, testMemory(ScopeSession, ScopeBinding{AnchorSessionID: "s1"}, "private session", ""), "alice", "agent", "s")
	if err != nil {
		t.Fatal(err)
	}
	authorized := QueryContext{Principal: "alice", SessionID: "s1"}
	if _, err := svc.Relate(ctx, Relation{FromID: global.ID, ToID: session.ID, Type: RelationRelated}, authorized, "alice", "agent", "rel"); err != nil {
		t.Fatal(err)
	}
	rels, err := svc.Relations(global.ID, authorized)
	if err != nil || len(rels) != 1 {
		t.Fatalf("rels=%v err=%v", rels, err)
	}
	sibling := QueryContext{Principal: "alice", SessionID: "sibling"}
	rels, err = svc.Relations(global.ID, sibling)
	if err != nil || len(rels) != 0 {
		t.Fatalf("hidden endpoint leaked: rels=%v err=%v", rels, err)
	}
	if _, err := svc.Relations(session.ID, sibling); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
