package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestServerEmitRenderPanel_RoundTrip: a render notification serialises
// to the expected `session/update kind=panel` wire shape with all
// section bodies preserved. F9b.3.
func TestServerEmitRenderPanel_RoundTrip(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(&config.Config{}, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), &out)

	srv.emitRenderPanel("session-xyz", pluginRuntime.Panel{
		Title:   "Recon results",
		Variant: "ok",
		Footer:  "next: pick a port",
		Sections: []pluginRuntime.Section{
			{Kind: "text", Heading: "Summary", Text: "3 hosts up"},
			{Kind: "kv", KV: []pluginRuntime.KVPair{{Label: "host", Value: "10.0.0.1"}}},
			{Kind: "list", List: pluginRuntime.ListBody{Marker: "numbered", Items: []string{"a", "b"}}},
			{Kind: "code", Code: pluginRuntime.CodeBody{Language: "go", Content: "fmt.Println"}},
			{Kind: "table", Table: pluginRuntime.TableBody{
				Columns: []string{"h", "p"},
				Rows:    [][]string{{"x", "1"}, {"y", "2"}},
			}},
			{Kind: "diff", Diff: pluginRuntime.DiffBody{Before: "old", After: "new"}},
		},
	})

	var got Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	if got.Method != "session/update" {
		t.Fatalf("method = %q, want session/update", got.Method)
	}
	params, ok := got.Params.(map[string]any)
	if !ok {
		t.Fatalf("params type = %T", got.Params)
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

	if panel["title"] != "Recon results" || panel["variant"] != "ok" || panel["footer"] != "next: pick a port" {
		t.Errorf("envelope mismatch: %#v", panel)
	}
	sections, ok := panel["sections"].([]any)
	if !ok || len(sections) != 6 {
		t.Fatalf("sections = %#v", panel["sections"])
	}
	// Walk each section; assert (a) the right body field is set,
	// (b) no foreign body fields leak through.
	expects := []struct {
		kind, body string
	}{
		{"text", "text"},
		{"kv", "kv"},
		{"list", "list"},
		{"code", "code"},
		{"table", "table"},
		{"diff", "diff"},
	}
	bodyFields := []string{"text", "kv", "list", "code", "table", "diff"}
	for i, want := range expects {
		sec, ok := sections[i].(map[string]any)
		if !ok {
			t.Fatalf("section %d type = %T", i, sections[i])
		}
		if sec["kind"] != want.kind {
			t.Errorf("section %d kind = %v, want %v", i, sec["kind"], want.kind)
		}
		// The right body field is present.
		if _, ok := sec[want.body]; !ok {
			t.Errorf("section %d missing %q body: %#v", i, want.body, sec)
		}
		// No foreign body fields.
		for _, bf := range bodyFields {
			if bf == want.body {
				continue
			}
			if _, ok := sec[bf]; ok {
				t.Errorf("section %d kind=%q must not carry foreign body %q: %#v",
					i, want.kind, bf, sec)
			}
		}
	}
}

// TestServerEmitRenderPanel_OmitsEmptyOptionalFields: variant / id /
// footer are optional — when empty they must not appear in the wire
// payload (avoids spurious empty strings on the client side). F9b.3.
func TestServerEmitRenderPanel_OmitsEmptyOptionalFields(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(&config.Config{}, scriptedProvider{text: "ok"})
	srv.conn = NewConn(strings.NewReader(""), &out)

	srv.emitRenderPanel("session-1", pluginRuntime.Panel{
		Title:    "minimal",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: "x"}},
	})
	var got Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	panel := got.Params.(map[string]any)["panel"].(map[string]any)
	for _, missing := range []string{"variant", "id", "footer"} {
		if _, present := panel[missing]; present {
			t.Errorf("expected %q to be omitted when empty; got: %#v", missing, panel)
		}
	}
	// Title and sections must always be present.
	if panel["title"] != "minimal" {
		t.Errorf("title = %v", panel["title"])
	}
	if _, ok := panel["sections"].([]any); !ok {
		t.Errorf("sections missing")
	}
}

// TestACPHostRender_NilServerDropsOnFloor: the ACP host's Render
// implementation is fire-and-forget. With no server attached it must
// return nil silently rather than erroring the plugin. F9b.3.
func TestACPHostRender_NilServerDropsOnFloor(t *testing.T) {
	h := &acpHost{} // server unset
	err := h.Render(t.Context(), pluginRuntime.Panel{
		Title:    "x",
		Sections: []pluginRuntime.Section{{Kind: "text", Text: "y"}},
	})
	if err != nil {
		t.Errorf("nil-server render should drop silently, got: %v", err)
	}
}
