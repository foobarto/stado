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
)

func TestMemoryCLI_ListApproveDelete(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()
	item := memory.Item{
		ID:      "mem_cli",
		Scope:   "global",
		Kind:    "preference",
		Summary: "Prefer focused implementation slices",
	}
	raw, _ := json.Marshal(item)
	if err := store.Propose(ctx, raw); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	memoryListJSON = false
	out := captureStdout(t, func() {
		if err := memoryListCmd.RunE(memoryListCmd, nil); err != nil {
			t.Fatalf("memory list: %v", err)
		}
	})
	if !strings.Contains(out, "mem_cli") || !strings.Contains(out, "candidate") {
		t.Fatalf("list did not include candidate memory:\n%s", out)
	}

	if err := memoryApproveCmd.RunE(memoryApproveCmd, []string{"mem_cli"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	result, err := store.Query(ctx, memory.Query{Prompt: "focused implementation"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Item.Confidence != "approved" {
		t.Fatalf("approved memory not queryable: %+v", result)
	}

	if err := memoryDeleteCmd.RunE(memoryDeleteCmd, []string{"mem_cli"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	result, err = store.Query(ctx, memory.Query{Prompt: "focused implementation"})
	if err != nil {
		t.Fatalf("Query after delete: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("deleted memory was still queryable: %+v", result)
	}
}

func TestMemoryCLI_ExportJSON(t *testing.T) {
	store := setupMemoryEnv(t)
	item := memory.Item{
		ID:         "mem_export",
		Scope:      "global",
		Kind:       "fact",
		Summary:    "Export me",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	out := captureStdout(t, func() {
		if err := memoryExportCmd.RunE(memoryExportCmd, nil); err != nil {
			t.Fatalf("memory export: %v", err)
		}
	})
	var exported memory.Export
	if err := json.Unmarshal([]byte(out), &exported); err != nil {
		t.Fatalf("export JSON: %v\n%s", err, out)
	}
	if len(exported.Items) != 1 || exported.Items[0].ID != "mem_export" {
		t.Fatalf("unexpected export: %+v", exported)
	}
}

func setupMemoryEnv(t *testing.T) *memory.Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := chdir(t, cwd)
	t.Cleanup(restore)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &memory.Store{
		Path:  filepath.Join(cfg.StateDir(), "memory", "memory.jsonl"),
		Actor: "test",
	}
}
