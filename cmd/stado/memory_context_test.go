package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestBuildMemoryPromptContextOptIn(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	workdir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(workdir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	store := memory.Store{Path: filepath.Join(cfg.StateDir(), "memory", "memory.jsonl"), Actor: "test"}
	item := memory.Item{
		ID:         "mem_run",
		Scope:      "repo",
		RepoID:     repoID,
		Kind:       "preference",
		Summary:    "Prefer short CLI answers",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatal(err)
	}

	if got := buildMemoryPromptContext(context.Background(), cfg, workdir, "", "short answer"); got != "" {
		t.Fatalf("disabled memory context = %q, want empty", got)
	}
	cfg.Memory.Enabled = true
	got := buildMemoryPromptContext(context.Background(), cfg, workdir, "", "short answer")
	if !strings.Contains(got, "[repo/preference mem_run] Prefer short CLI answers") {
		t.Fatalf("memory context missing approved item:\n%s", got)
	}
}
