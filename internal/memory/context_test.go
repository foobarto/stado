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
