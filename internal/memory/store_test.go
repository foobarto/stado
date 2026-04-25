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

func TestStoreSupersedeKeepsTombstoneAndQueriesReplacement(t *testing.T) {
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	ctx := context.Background()
	item := Item{
		ID:         "mem_supersede",
		Scope:      "global",
		Kind:       "preference",
		Summary:    "Old memory summary",
		Body:       "Keep the old wording.",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(ctx, raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	store.Now = func() time.Time {
		return now.Add(time.Hour)
	}
	replacement := Item{
		ID:         "mem_supersede_next",
		Scope:      "global",
		Kind:       "preference",
		Summary:    "New memory summary",
		Body:       "Use the replacement wording.",
		Confidence: "approved",
	}
	updateRaw, _ := json.Marshal(UpdateRequest{Action: "supersede", ID: "mem_supersede", Item: &replacement})
	if err := store.Update(ctx, updateRaw); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	old, ok, err := store.Show(ctx, "mem_supersede")
	if err != nil {
		t.Fatalf("Show old: %v", err)
	}
	if !ok {
		t.Fatal("superseded memory missing")
	}
	if old.Confidence != "superseded" {
		t.Fatalf("old confidence = %q, want superseded", old.Confidence)
	}

	next, ok, err := store.Show(ctx, "mem_supersede_next")
	if err != nil {
		t.Fatalf("Show replacement: %v", err)
	}
	if !ok {
		t.Fatal("replacement memory missing")
	}
	if next.Confidence != "approved" || next.Summary != "New memory summary" {
		t.Fatalf("unexpected replacement: %+v", next)
	}
	if len(next.Supersedes) != 1 || next.Supersedes[0] != "mem_supersede" {
		t.Fatalf("replacement supersedes = %#v", next.Supersedes)
	}

	got, err := store.Query(ctx, Query{Prompt: "memory summary replacement"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Item.ID != "mem_supersede_next" {
		t.Fatalf("query returned old or missing replacement: %+v", got.Items)
	}

	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"action":"supersede"`) {
		t.Fatalf("append log missing supersede event:\n%s", string(data))
	}
}

func TestStoreSupersedeRequiresApprovedMemory(t *testing.T) {
	store := testStore(t, time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()
	item := Item{
		ID:         "mem_candidate",
		Scope:      "global",
		Kind:       "fact",
		Summary:    "Candidate memory",
		Confidence: "candidate",
	}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(ctx, raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	replacement := Item{
		Scope:   "global",
		Kind:    "fact",
		Summary: "Replacement memory",
	}
	updateRaw, _ := json.Marshal(UpdateRequest{Action: "supersede", ID: "mem_candidate", Item: &replacement})
	err := store.Update(ctx, updateRaw)
	if err == nil || !strings.Contains(err.Error(), "only approved memories can be superseded") {
		t.Fatalf("expected approved-memory validation error, got %v", err)
	}
}

func TestStoreLessonValidationAndQueryFilter(t *testing.T) {
	store := testStore(t, time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()

	invalid := Item{
		MemoryKind: "lesson",
		Scope:      "global",
		Summary:    "Use pinned Go toolchain",
		Lesson:     "Use the pinned toolchain path when go is absent from PATH.",
	}
	raw, _ := json.Marshal(invalid)
	err := store.Propose(ctx, raw)
	if err == nil || !strings.Contains(err.Error(), "trigger is required") {
		t.Fatalf("expected trigger validation error, got %v", err)
	}

	invalid.Trigger = "When Go tooling is not on PATH"
	invalid.Source = Source{SessionID: "source-session"}
	raw, _ = json.Marshal(invalid)
	err = store.Propose(ctx, raw)
	if err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("expected evidence validation error, got %v", err)
	}
	invalid.Source = Source{}

	ordinary := Item{
		ID:         "mem_ordinary",
		Scope:      "global",
		Kind:       "preference",
		Summary:    "Prefer short diffs",
		Confidence: "approved",
	}
	ordinaryRaw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &ordinary})
	if err := store.Update(ctx, ordinaryRaw); err != nil {
		t.Fatalf("ordinary upsert: %v", err)
	}

	lesson := Item{
		MemoryKind: "lesson",
		Scope:      "global",
		Summary:    "Use pinned Go toolchain",
		Lesson:     "Use the pinned toolchain path when go is absent from PATH.",
		Trigger:    "When Go tooling is not on PATH",
		Evidence:   Evidence{Tests: []string{"go test ./..."}},
		Confidence: "approved",
	}
	lessonRaw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &lesson})
	if err := store.Update(ctx, lessonRaw); err != nil {
		t.Fatalf("lesson upsert: %v", err)
	}

	lessons, err := store.Query(ctx, Query{Prompt: "go tooling path", MemoryKind: "lesson"})
	if err != nil {
		t.Fatalf("lesson query: %v", err)
	}
	if len(lessons.Items) != 1 || !IsLesson(lessons.Items[0].Item) {
		t.Fatalf("expected one lesson, got %+v", lessons.Items)
	}
	if !strings.HasPrefix(lessons.Items[0].Item.ID, "lesson_") {
		t.Fatalf("generated lesson id = %q, want lesson_ prefix", lessons.Items[0].Item.ID)
	}

	lessonWithLargeEvidence := Item{
		ID:         "lesson_budget",
		MemoryKind: "lesson",
		Scope:      "global",
		Summary:    "Budget uses rendered lesson text",
		Lesson:     "Short lesson that fits the prompt budget.",
		Trigger:    "When estimating prompt memory",
		Evidence:   Evidence{Notes: strings.Repeat("verbose evidence ", 80)},
		Confidence: "approved",
	}
	lessonBudgetRaw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &lessonWithLargeEvidence})
	if err := store.Update(ctx, lessonBudgetRaw); err != nil {
		t.Fatalf("lesson budget upsert: %v", err)
	}
	budgetedLessons, err := store.Query(ctx, Query{Prompt: "budget rendered lesson", MemoryKind: "lesson", BudgetTokens: 40})
	if err != nil {
		t.Fatalf("budgeted lesson query: %v", err)
	}
	if !containsRankedItem(budgetedLessons.Items, lessonWithLargeEvidence.ID) {
		t.Fatalf("rendered lesson should fit budget despite large evidence, got %+v", budgetedLessons.Items)
	}

	memories, err := store.Query(ctx, Query{Prompt: "short diffs", MemoryKind: "memory"})
	if err != nil {
		t.Fatalf("memory query: %v", err)
	}
	if len(memories.Items) != 1 || memories.Items[0].Item.ID != "mem_ordinary" {
		t.Fatalf("expected ordinary memory only, got %+v", memories.Items)
	}

	defaultQuery, err := store.Query(ctx, Query{Prompt: "go tooling path"})
	if err != nil {
		t.Fatalf("default query: %v", err)
	}
	if len(defaultQuery.Items) != 1 || IsLesson(defaultQuery.Items[0].Item) {
		t.Fatalf("default query should exclude lessons, got %+v", defaultQuery.Items)
	}

	explicitMemory := Item{
		ID:          "mem_kind_lesson_label",
		MemoryKind:  "memory",
		Scope:       "global",
		Kind:        "lesson",
		Summary:     "Ordinary memory with a lesson label",
		Confidence:  "approved",
		Sensitivity: "normal",
	}
	explicitMemoryRaw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &explicitMemory})
	if err := store.Update(ctx, explicitMemoryRaw); err != nil {
		t.Fatalf("explicit memory upsert: %v", err)
	}
	memories, err = store.Query(ctx, Query{Prompt: "ordinary memory label", MemoryKind: "memory"})
	if err != nil {
		t.Fatalf("explicit memory query: %v", err)
	}
	if !containsRankedItem(memories.Items, explicitMemory.ID) {
		t.Fatalf("explicit memory_kind=memory should stay ordinary, got %+v", memories.Items)
	}
	lessons, err = store.Query(ctx, Query{Prompt: "ordinary memory label", MemoryKind: "lesson"})
	if err != nil {
		t.Fatalf("lesson query after explicit memory: %v", err)
	}
	if containsRankedItem(lessons.Items, explicitMemory.ID) {
		t.Fatalf("explicit memory_kind=memory leaked into lesson query: %+v", lessons.Items)
	}

	legacyKindMemory := Item{
		ID:         "mem_legacy_lesson_kind",
		Scope:      "global",
		Kind:       "lesson",
		Summary:    "Ordinary memory with no memory kind",
		Confidence: "approved",
	}
	legacyKindRaw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &legacyKindMemory})
	if err := store.Update(ctx, legacyKindRaw); err != nil {
		t.Fatalf("legacy kind memory upsert: %v", err)
	}
	memories, err = store.Query(ctx, Query{Prompt: "ordinary memory kind", MemoryKind: "memory"})
	if err != nil {
		t.Fatalf("legacy kind memory query: %v", err)
	}
	if !containsRankedItem(memories.Items, legacyKindMemory.ID) {
		t.Fatalf("kind=lesson without memory_kind should stay ordinary, got %+v", memories.Items)
	}
	lessons, err = store.Query(ctx, Query{Prompt: "ordinary memory kind", MemoryKind: "lesson"})
	if err != nil {
		t.Fatalf("lesson query after legacy kind memory: %v", err)
	}
	if containsRankedItem(lessons.Items, legacyKindMemory.ID) {
		t.Fatalf("kind=lesson without memory_kind leaked into lesson query: %+v", lessons.Items)
	}

	_, err = store.Query(ctx, Query{MemoryKind: "unknown"})
	if err == nil || !strings.Contains(err.Error(), "invalid memory_kind") {
		t.Fatalf("expected invalid memory_kind error, got %v", err)
	}
}

func containsRankedItem(items []RankedItem, id string) bool {
	for _, item := range items {
		if item.Item.ID == id {
			return true
		}
	}
	return false
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
