package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/memory"
)

func TestLocalMemoryBridgeRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	bridge := NewLocalMemoryBridge(stateDir, "plugin:test")
	bridge.Store.Path = filepath.Join(stateDir, "memory.jsonl")

	item := memory.Item{
		ID:      "mem_bridge",
		Scope:   "repo",
		RepoID:  "repo-1",
		Kind:    "preference",
		Summary: "Prefer focused tests",
	}
	raw, _ := json.Marshal(item)
	if err := bridge.Propose(context.Background(), raw); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	approve, _ := json.Marshal(memory.UpdateRequest{Action: "approve", ID: item.ID})
	if err := bridge.Update(context.Background(), approve); err != nil {
		t.Fatalf("Update approve: %v", err)
	}

	query, _ := json.Marshal(memory.Query{RepoID: "repo-1", Prompt: "focused tests"})
	resultRaw, err := bridge.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var result memory.QueryResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Item.ID != item.ID {
		t.Fatalf("unexpected query result: %+v", result)
	}
}
