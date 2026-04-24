package acp

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

func TestACPMemoryPromptContextUsesApprovedMemory(t *testing.T) {
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
	cfg.Memory.Enabled = true

	store := memory.Store{Path: filepath.Join(cfg.StateDir(), "memory", "memory.jsonl"), Actor: "test"}
	item := memory.Item{
		ID:         "mem_acp",
		Scope:      "repo",
		RepoID:     repoID,
		Kind:       "preference",
		Summary:    "Prefer compact protocol replies",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(cfg, nil)
	got := srv.memoryPromptContext(context.Background(), workdir, "acp-1", "write a compact reply")
	if !strings.Contains(got, "[repo/preference mem_acp] Prefer compact protocol replies") {
		t.Fatalf("memory context missing approved item:\n%s", got)
	}
}
