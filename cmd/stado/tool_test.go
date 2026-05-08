package main

import (
	"encoding/json"
	"testing"
)

// TestToolListJSON_ShapeStable: `stado tool list --json` must emit
// a single valid JSON document — pre-v0.46.2 emitted NDJSON which
// broke `python3 -m json.tool`, `jq .`, and any strict-JSON parser.
// Also asserts the schema_version + tools[] envelope shape so the
// stability commitment from CHANGELOG.md is testable in CI.
func TestToolListJSON_ShapeStable(t *testing.T) {
	in := toolListJSON{
		SchemaVersion: ToolListJSONSchemaVersion,
		Count:         2,
		Tools: []toolListEntry{
			{Name: "fs.read", State: "autoloaded", Plugin: "", Categories: "filesystem"},
			{Name: "exec", State: "enabled", Plugin: "shell", Categories: "shell"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not a single JSON document (regression — was NDJSON): %v\n%s", err, b)
	}
	if sv, ok := got["schema_version"].(float64); !ok || int(sv) != ToolListJSONSchemaVersion {
		t.Errorf("schema_version: got %v, want %d", got["schema_version"], ToolListJSONSchemaVersion)
	}
	if c, _ := got["count"].(float64); int(c) != 2 {
		t.Errorf("count: got %v, want 2", got["count"])
	}
	tools, ok := got["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing or wrong type: %v", got["tools"])
	}
	if len(tools) != 2 {
		t.Fatalf("tools length: got %d, want 2", len(tools))
	}
	first, _ := tools[0].(map[string]any)
	for _, key := range []string{"name", "state", "plugin", "categories"} {
		if _, present := first[key]; !present {
			t.Errorf("tool entry missing key %q: %v", key, first)
		}
	}
	if first["name"] != "fs.read" {
		t.Errorf("tools[0].name: got %v, want fs.read", first["name"])
	}
}

// TestToolListJSON_EmptyToolsArray: zero-row case still emits a
// valid envelope with `"tools": []` (not null), so consumers can
// always iterate the array without nil-checking.
func TestToolListJSON_EmptyToolsArray(t *testing.T) {
	in := toolListJSON{
		SchemaVersion: ToolListJSONSchemaVersion,
		Count:         0,
		Tools:         []toolListEntry{},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	tools, ok := got["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not an array (got %T): %s", got["tools"], b)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools array, got %d entries", len(tools))
	}
}
