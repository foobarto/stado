package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestPromptContextDisabled(t *testing.T) {
	got, err := PromptContext(context.Background(), PromptContextOptions{Enabled: false, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("disabled memory context = %q, want empty", got)
	}
}

func TestPromptContextFormatsApprovedScopedMemories(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(workdir)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(root, "state", "memory", "memory.jsonl"), Actor: "test"}
	item := Item{
		ID:         "mem_prompt",
		Scope:      "repo",
		RepoID:     repoID,
		Kind:       "preference",
		Summary:    "Prefer surgical diffs",
		Body:       "Keep edits small.",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := PromptContext(context.Background(), PromptContextOptions{
		Enabled:      true,
		StateDir:     filepath.Join(root, "state"),
		Workdir:      filepath.Join(workdir, "sub"),
		Prompt:       "please make surgical changes",
		MaxItems:     4,
		BudgetTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Memory snippets supplied by installed plugins",
		"[repo/preference mem_prompt] Prefer surgical diffs",
		"Keep edits small.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("memory context missing %q:\n%s", want, got)
		}
	}
}

func TestPromptContextSeparatesApprovedLessons(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(workdir)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(root, "state", "memory", "memory.jsonl"), Actor: "test"}
	memoryItem := Item{
		ID:         "mem_prompt_regular",
		Scope:      "repo",
		RepoID:     repoID,
		Kind:       "preference",
		Summary:    "Prefer focused verification",
		Confidence: "approved",
	}
	lessonItem := Item{
		ID:         "lesson_prompt",
		MemoryKind: "lesson",
		Scope:      "repo",
		RepoID:     repoID,
		Summary:    "Use pinned Go toolchain",
		Lesson:     "Use the pinned toolchain path before declaring Go unavailable.",
		Trigger:    "When Go is missing from PATH",
		Evidence:   Evidence{Tests: []string{"go test ./..."}},
		Confidence: "approved",
	}
	for _, item := range []Item{memoryItem, lessonItem} {
		raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
		if err := store.Update(context.Background(), raw); err != nil {
			t.Fatalf("upsert %s: %v", item.ID, err)
		}
	}

	got, err := PromptContext(context.Background(), PromptContextOptions{
		Enabled:      true,
		StateDir:     filepath.Join(root, "state"),
		Workdir:      workdir,
		Prompt:       "go path focused verification",
		MaxItems:     4,
		BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Memory snippets supplied by installed plugins",
		"[repo/preference mem_prompt_regular] Prefer focused verification",
		"Operational lessons from prior approved sessions",
		"[repo/lesson lesson_prompt] Use pinned Go toolchain",
		"trigger: When Go is missing from PATH",
		"lesson: Use the pinned toolchain path before declaring Go unavailable.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt context missing %q:\n%s", want, got)
		}
	}
}

func TestPromptContextUsesEditedCanonicalLesson(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(workdir)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(root, "state", "memory", "memory.jsonl"), Actor: "test"}
	lesson := Item{
		ID:         "lesson_edit_prompt",
		MemoryKind: "lesson",
		Scope:      "repo",
		RepoID:     repoID,
		Summary:    "Keep lesson edits reviewable",
		Body:       "Old lesson body.",
		Lesson:     "Old lesson body.",
		Trigger:    "When a lesson candidate is edited",
		Evidence:   Evidence{Notes: "unit test"},
		Confidence: "candidate",
	}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &lesson})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	lesson.Lesson = "Edited lesson body."
	editRaw, _ := json.Marshal(UpdateRequest{Action: "edit", ID: lesson.ID, Item: &lesson})
	if err := store.Update(context.Background(), editRaw); err != nil {
		t.Fatalf("edit: %v", err)
	}
	approveRaw, _ := json.Marshal(UpdateRequest{Action: "approve", ID: lesson.ID})
	if err := store.Update(context.Background(), approveRaw); err != nil {
		t.Fatalf("approve: %v", err)
	}

	got, err := PromptContext(context.Background(), PromptContextOptions{
		Enabled:      true,
		StateDir:     filepath.Join(root, "state"),
		Workdir:      workdir,
		Prompt:       "reviewable lesson edits",
		MaxItems:     4,
		BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "lesson: Edited lesson body.") {
		t.Fatalf("prompt context missing edited lesson body:\n%s", got)
	}
	if strings.Contains(got, "lesson: Old lesson body.") {
		t.Fatalf("prompt context used stale lesson body:\n%s", got)
	}
}

func TestPromptContextAppliesCombinedItemCap(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(workdir)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(root, "state", "memory", "memory.jsonl"), Actor: "test"}
	items := []Item{
		{
			ID:         "mem_cap",
			Scope:      "repo",
			RepoID:     repoID,
			Kind:       "preference",
			Summary:    "Prefer focused verification",
			Confidence: "approved",
		},
		{
			ID:         "lesson_cap",
			MemoryKind: "lesson",
			Scope:      "repo",
			RepoID:     repoID,
			Summary:    "Use pinned Go toolchain",
			Lesson:     "Use the pinned toolchain path before declaring Go unavailable.",
			Trigger:    "When Go is missing from PATH",
			Evidence:   Evidence{Tests: []string{"go test ./..."}},
			Confidence: "approved",
		},
	}
	for _, item := range items {
		raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
		if err := store.Update(context.Background(), raw); err != nil {
			t.Fatalf("upsert %s: %v", item.ID, err)
		}
	}

	got, err := PromptContext(context.Background(), PromptContextOptions{
		Enabled:      true,
		StateDir:     filepath.Join(root, "state"),
		Workdir:      workdir,
		Prompt:       "go focused verification",
		MaxItems:     1,
		BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bullets := strings.Count(got, "\n- ["); bullets != 1 {
		t.Fatalf("expected one prompt item with combined cap, got %d:\n%s", bullets, got)
	}
}

func TestPromptContextUsesOriginalWorkdirOutsideGit(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "not-git")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(workdir)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(root, "state", "memory", "memory.jsonl"), Actor: "test"}
	item := Item{
		ID:         "mem_nongit",
		Scope:      "repo",
		RepoID:     repoID,
		Kind:       "fact",
		Summary:    "Non-git cwd memory",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := PromptContext(context.Background(), PromptContextOptions{
		Enabled:      true,
		StateDir:     filepath.Join(root, "state"),
		Workdir:      workdir,
		Prompt:       "memory",
		MaxItems:     4,
		BudgetTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[repo/fact mem_nongit] Non-git cwd memory") {
		t.Fatalf("memory context missing non-git item:\n%s", got)
	}
}
