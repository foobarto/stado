package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreProposeApproveQuery(t *testing.T) {
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	ctx := context.Background()

	item := Item{
		ID:          "mem_surgical",
		Scope:       "repo",
		RepoID:      "repo-1",
		Kind:        "preference",
		Summary:     "Prefer surgical diffs",
		Body:        "Keep edits narrow and match the surrounding code.",
		Sensitivity: "normal",
	}
	raw, _ := json.Marshal(item)
	if err := store.Propose(ctx, raw); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	before, err := store.Query(ctx, Query{RepoID: "repo-1", Prompt: "surgical diff"})
	if err != nil {
		t.Fatalf("Query before approval: %v", err)
	}
	if len(before.Items) != 0 {
		t.Fatalf("candidate memory should not be returned before approval: %+v", before.Items)
	}

	approve, _ := json.Marshal(UpdateRequest{Action: "approve", ID: "mem_surgical"})
	if err := store.Update(ctx, approve); err != nil {
		t.Fatalf("approve: %v", err)
	}

	got, err := store.Query(ctx, Query{RepoID: "repo-1", Prompt: "surgical diff", MaxItems: 4})
	if err != nil {
		t.Fatalf("Query after approval: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected one approved memory, got %+v", got.Items)
	}
	if got.Items[0].Item.ID != "mem_surgical" || got.Items[0].Item.Confidence != "approved" {
		t.Fatalf("unexpected memory: %+v", got.Items[0].Item)
	}

	otherRepo, err := store.Query(ctx, Query{RepoID: "repo-2", Prompt: "surgical diff"})
	if err != nil {
		t.Fatalf("Query other repo: %v", err)
	}
	if len(otherRepo.Items) != 0 {
		t.Fatalf("repo-scoped memory leaked to another repo: %+v", otherRepo.Items)
	}
}

func TestStoreFiltersSecretAndBudget(t *testing.T) {
	store := testStore(t, time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()
	for _, item := range []Item{
		{ID: "mem_normal", Scope: "global", Kind: "fact", Summary: "Use Go tests", Body: "go test ./...", Confidence: "approved"},
		{ID: "mem_secret", Scope: "global", Kind: "fact", Summary: "Secret token", Body: "never expose", Confidence: "approved", Sensitivity: "secret"},
		{ID: "mem_large", Scope: "global", Kind: "fact", Summary: "Large entry", Body: strings.Repeat("x", 200), Confidence: "approved"},
	} {
		raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
		if err := store.Update(ctx, raw); err != nil {
			t.Fatalf("upsert %s: %v", item.ID, err)
		}
	}

	got, err := store.Query(ctx, Query{Prompt: "go tests secret large", BudgetTokens: 16, MaxItems: 8})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected only non-secret item within budget, got %+v", got.Items)
	}
	if got.Items[0].Item.ID != "mem_normal" {
		t.Fatalf("unexpected item returned: %+v", got.Items[0].Item)
	}
}

func TestStoreDeleteHidesItemButKeepsAppendOnlyLog(t *testing.T) {
	store := testStore(t, time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()
	item := Item{ID: "mem_delete", Scope: "global", Kind: "fact", Summary: "Temporary fact", Confidence: "approved"}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(ctx, raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	del, _ := json.Marshal(UpdateRequest{Action: "delete", ID: item.ID})
	if err := store.Update(ctx, del); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := store.Query(ctx, Query{Prompt: "temporary"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("deleted memory returned: %+v", got.Items)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 2 {
		t.Fatalf("expected two append-only events, got %d in %q", lines, string(data))
	}
}

func TestStoreEditReplacesItemAppendOnly(t *testing.T) {
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	ctx := context.Background()
	item := Item{
		ID:         "mem_edit",
		Scope:      "global",
		Kind:       "preference",
		Summary:    "Old summary",
		Body:       "Old body",
		Confidence: "candidate",
	}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(ctx, raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	before, ok, err := store.Show(ctx, "mem_edit")
	if err != nil {
		t.Fatalf("Show before edit: %v", err)
	}
	if !ok {
		t.Fatal("memory missing before edit")
	}
	createdAt := before.CreatedAt

	store.Now = func() time.Time {
		return now.Add(time.Hour)
	}
	edited := Item{
		ID:         "mem_edit",
		Scope:      "global",
		Kind:       "preference",
		Summary:    "Edited summary",
		Body:       "Edited body",
		Confidence: "candidate",
	}
	editRaw, _ := json.Marshal(UpdateRequest{Action: "edit", ID: "mem_edit", Item: &edited})
	if err := store.Update(ctx, editRaw); err != nil {
		t.Fatalf("edit: %v", err)
	}

	got, ok, err := store.Show(ctx, "mem_edit")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !ok {
		t.Fatal("edited memory missing")
	}
	if got.Summary != "Edited summary" || got.Body != "Edited body" {
		t.Fatalf("edit did not replace item: %+v", got)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %s, want %s", got.CreatedAt, createdAt)
	}
	if !got.UpdatedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("updated_at = %s, want %s", got.UpdatedAt, now.Add(time.Hour))
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"action":"edit"`) {
		t.Fatalf("append log missing edit event:\n%s", string(data))
	}
}

func TestStoreRejectsInvalidScope(t *testing.T) {
	store := testStore(t, time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	raw, _ := json.Marshal(Item{ID: "mem_bad", Scope: "repo", Summary: "Missing repo"})
	err := store.Propose(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "repo_id is required") {
		t.Fatalf("expected repo_id validation error, got %v", err)
	}
}

func testStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	return &Store{
		Path:  filepath.Join(t.TempDir(), "memory.jsonl"),
		Actor: "test",
		Now: func() time.Time {
			return now
		},
	}
}
