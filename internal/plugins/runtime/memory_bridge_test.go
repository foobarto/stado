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

// A plugin must not be able to read session-scoped memories by forging a
// session_id (or allowed_scopes) in its query JSON. The bridge default-denies
// session scope: it clears SessionID/ancestry and pins scopes to repo+global.
func TestLocalMemoryBridgeDeniesForgedSessionScope(t *testing.T) {
	stateDir := t.TempDir()
	bridge := NewLocalMemoryBridge(stateDir, "plugin:test")
	bridge.Store.Path = filepath.Join(stateDir, "memory.jsonl")
	ctx := context.Background()

	seed := func(item memory.Item) {
		raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
		if err := bridge.Update(ctx, raw); err != nil {
			t.Fatalf("seed %s: %v", item.ID, err)
		}
	}
	seed(memory.Item{ID: "mem_victim", Scope: "session", SessionID: "victim", Kind: "fact", Summary: "victim branch secret-ish decision", Confidence: "approved"})
	seed(memory.Item{ID: "mem_repo", Scope: "repo", RepoID: "repo-1", Kind: "fact", Summary: "repo wide fact", Confidence: "approved"})
	seed(memory.Item{ID: "mem_global", Scope: "global", Kind: "fact", Summary: "global fact", Confidence: "approved"})

	ids := func(q memory.Query) map[string]bool {
		raw, _ := json.Marshal(q)
		out, err := bridge.Query(ctx, raw)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		var r memory.QueryResult
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]bool{}
		for _, it := range r.Items {
			if it.Item.Scope == "session" || it.Item.ID == "mem_victim" {
				t.Fatalf("plugin read a session-scoped memory: %+v", it.Item)
			}
			got[it.Item.ID] = true
		}
		return got
	}

	// Forged session query must not surface the victim's session memory, even
	// when the plugin also asks for session scope (the helper fails on any
	// session item).
	ids(memory.Query{SessionID: "victim", AllowedScopes: []string{"session"}, Prompt: "victim branch decision"})

	// Repo + global reads still work for plugins (global matches every query).
	got := ids(memory.Query{RepoID: "repo-1", Prompt: "repo wide fact global fact"})
	if !got["mem_repo"] {
		t.Fatalf("plugin repo-scope read broken: %v", got)
	}
	if !got["mem_global"] {
		t.Fatalf("plugin global-scope read broken: %v", got)
	}
}
