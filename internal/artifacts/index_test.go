package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexRebuildScopeSensitivityAndStaleness(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	normal, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeRepo, Binding: ScopeBinding{CanonicalRepoID: "repo-a"}, Summary: "Malformed JSON arguments", Content: "retry with valid json"}, "alice", "agent", "normal")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeRepo, Binding: ScopeBinding{CanonicalRepoID: "repo-a"}, Summary: "Private JSON password", Sensitivity: "private"}, "alice", "agent", "private")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	idx, err := OpenIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Rebuild(store.Records()); err != nil {
		t.Fatal(err)
	}
	q := Query{Context: QueryContext{Principal: "alice", CanonicalRepoID: "repo-a"}}
	got, err := idx.Search(ctx, svc, "JSON", q)
	if err != nil || len(got) != 1 || got[0].ID != normal.ID {
		t.Fatalf("got=%v err=%v", got, err)
	}
	got, err = idx.Search(ctx, svc, "JSON", Query{Context: QueryContext{Principal: "alice", CanonicalRepoID: "repo-b"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("cross repo got=%v err=%v", got, err)
	}
	if _, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeGlobal, Summary: "new JSON"}, "alice", "agent", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Search(ctx, svc, "JSON", q); !errors.Is(err, ErrIndexStale) {
		t.Fatalf("got %v", err)
	}
}

func TestIndexCanBeDeletedAndRebuilt(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	ctx := context.Background()
	if _, err := svc.Create(ctx, Artifact{Kind: KindMemory, Scope: ScopeGlobal, Summary: "rebuild needle"}, "alice", "agent", "create"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sqlite")
	idx, err := OpenIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Rebuild(store.Records()); err != nil {
		t.Fatal(err)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	idx, err = OpenIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Rebuild(store.Records()); err != nil {
		t.Fatal(err)
	}
	got, err := idx.Search(ctx, svc, "needle", Query{Context: QueryContext{Principal: "alice"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
}
