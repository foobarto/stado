package headless

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// F9b.5 unit tests for the headless render bridge.

// TestHeadlessRenderBridge_EmitsKindPanel: Render() pushes the panel
// out as a `session.update` notification with kind=panel and the
// session id intact. Wire shape mirrors ACP's kind=panel from F9b.3
// (so a client that decodes one decodes the other).
func TestHeadlessRenderBridge_EmitsKindPanel(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(&config.Config{}, nil)
	srv.conn = acp.NewConn(strings.NewReader(""), &out)

	bridge := &headlessRenderBridge{server: srv, sessionID: "session-xyz"}
	err := bridge.Render(t.Context(), pluginRuntime.Panel{
		Title:   "Recon",
		Variant: "ok",
		Footer:  "next",
		Sections: []pluginRuntime.Section{
			{Kind: "text", Heading: "S", Text: "narrative"},
			{Kind: "kv", KV: []pluginRuntime.KVPair{{Label: "host", Value: "10.0.0.1"}}},
			{Kind: "list", List: pluginRuntime.ListBody{Marker: "numbered", Items: []string{"a"}}},
			{Kind: "code", Code: pluginRuntime.CodeBody{Language: "go", Content: "x"}},
			{Kind: "table", Table: pluginRuntime.TableBody{Columns: []string{"c"}, Rows: [][]string{{"v"}}}},
			{Kind: "diff", Diff: pluginRuntime.DiffBody{Before: "old", After: "new"}},
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var msg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &msg); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	if msg["method"] != "session.update" {
		t.Errorf("method = %v, want session.update", msg["method"])
	}
	params, ok := msg["params"].(map[string]any)
	if !ok {
		t.Fatalf("params type = %T", msg["params"])
	}
	if params["sessionId"] != "session-xyz" {
		t.Errorf("sessionId = %v", params["sessionId"])
	}
	if params["kind"] != "panel" {
		t.Errorf("kind = %v, want panel", params["kind"])
	}
	panel, ok := params["panel"].(map[string]any)
	if !ok {
		t.Fatalf("panel type = %T", params["panel"])
	}
	if panel["title"] != "Recon" || panel["variant"] != "ok" || panel["footer"] != "next" {
		t.Errorf("envelope mismatch: %#v", panel)
	}
	sections, _ := panel["sections"].([]any)
	if len(sections) != 6 {
		t.Fatalf("sections = %d, want 6", len(sections))
	}
	bodyFields := []string{"text", "kv", "list", "code", "table", "diff"}
	for i, want := range bodyFields {
		sec := sections[i].(map[string]any)
		if sec["kind"] != want {
			t.Errorf("section %d kind = %v, want %v", i, sec["kind"], want)
		}
		if _, ok := sec[want]; !ok {
			t.Errorf("section %d missing %q body: %#v", i, want, sec)
		}
		for _, bf := range bodyFields {
			if bf == want {
				continue
			}
			if _, present := sec[bf]; present {
				t.Errorf("section %d kind=%q must not carry foreign body %q", i, want, bf)
			}
		}
	}
}

// TestHeadlessRenderBridge_OmitsEmptyOptionalFields: variant / id /
// footer absent when empty (matches the ACP and MCP precedent —
// noise-free wire). F9b.5.
func TestHeadlessRenderBridge_OmitsEmptyOptionalFields(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(&config.Config{}, nil)
	srv.conn = acp.NewConn(strings.NewReader(""), &out)

	bridge := &headlessRenderBridge{server: srv, sessionID: "s1"}
	if err := bridge.Render(t.Context(), pluginRuntime.Panel{
		Title:    "minimal",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &msg); err != nil {
		t.Fatal(err)
	}
	panel := msg["params"].(map[string]any)["panel"].(map[string]any)
	for _, missing := range []string{"variant", "id", "footer"} {
		if _, present := panel[missing]; present {
			t.Errorf("expected %q to be omitted when empty: %#v", missing, panel)
		}
	}
}

// TestHeadlessRenderBridge_NilServerDropsOnFloor: nil bridge / nil
// server / nil conn all return nil silently — fire-and-forget
// contract holds when the server is mid-teardown. F9b.5.
func TestHeadlessRenderBridge_NilServerDropsOnFloor(t *testing.T) {
	var b *headlessRenderBridge // nil receiver
	if err := b.Render(t.Context(), pluginRuntime.Panel{Title: "x"}); err != nil {
		t.Errorf("nil bridge should drop silently, got: %v", err)
	}

	b = &headlessRenderBridge{server: nil, sessionID: "s"}
	if err := b.Render(t.Context(), pluginRuntime.Panel{Title: "x"}); err != nil {
		t.Errorf("nil server should drop silently, got: %v", err)
	}

	srv := NewServer(&config.Config{}, nil)
	// conn unset — same as a torn-down server
	b = &headlessRenderBridge{server: srv, sessionID: "s"}
	if err := b.Render(t.Context(), pluginRuntime.Panel{Title: "x"}); err != nil {
		t.Errorf("nil conn should drop silently, got: %v", err)
	}
}
