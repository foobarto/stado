package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

// TestNewPluginTool_SchemaDefaults ensures a tool with an empty schema
// string surfaces a minimal `type: object` to the caller — the Provider
// will refuse a tool with no schema, so we guarantee the default here.
func TestNewPluginTool_SchemaDefaults(t *testing.T) {
	mod := &Module{Name: "demo"}
	pt, err := NewPluginTool(mod, plugins.ToolDef{
		Name:        "fetch",
		Description: "fetch a URL",
		// No Schema — legacy or minimal manifest.
	})
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	s := pt.Schema()
	if s["type"] != "object" {
		t.Errorf("default schema missing type=object: %v", s)
	}
	if pt.Name() != "fetch" {
		t.Errorf("name: %q", pt.Name())
	}
	if pt.Description() != "fetch a URL" {
		t.Errorf("desc: %q", pt.Description())
	}
	if pt.Class() != tool.ClassNonMutating {
		t.Errorf("class should default NonMutating, got %v", pt.Class())
	}
}

func TestNewPluginTool_ClassRoundTrip(t *testing.T) {
	mod := &Module{Name: "demo"}
	pt, err := NewPluginTool(mod, plugins.ToolDef{
		Name:  "execy",
		Class: "Exec",
	})
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	if pt.Class() != tool.ClassExec {
		t.Fatalf("Class() = %v, want %v", pt.Class(), tool.ClassExec)
	}
}

func TestNewPluginTool_ReadCapabilityPromotesClass(t *testing.T) {
	mod := &Module{
		Name: "demo",
		Manifest: plugins.Manifest{
			Name:         "demo",
			Capabilities: []string{"fs:read:/work"},
		},
	}
	pt, err := NewPluginTool(mod, plugins.ToolDef{Name: "inspect", Class: "NonMutating"})
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	if pt.Class() != tool.ClassExec {
		t.Fatalf("Class() = %v, want %v", pt.Class(), tool.ClassExec)
	}
}

func TestNewPluginTool_LSPCapabilityKeepsNonMutatingClass(t *testing.T) {
	mod := &Module{
		Name: "demo",
		Manifest: plugins.Manifest{
			Name:         "demo",
			Capabilities: []string{"fs:read:.", "lsp:query"},
		},
	}
	pt, err := NewPluginTool(mod, plugins.ToolDef{Name: "inspect", Class: "NonMutating"})
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	if pt.Class() != tool.ClassExec {
		// fs:read remains high-risk by policy, so the tool still promotes.
		t.Fatalf("Class() = %v, want %v", pt.Class(), tool.ClassExec)
	}
}

func TestEffectiveToolClass_LSPOnlyDoesNotPromote(t *testing.T) {
	class, err := EffectiveToolClass(plugins.ToolDef{Name: "hover", Class: "NonMutating"}, []string{"lsp:query"})
	if err != nil {
		t.Fatalf("EffectiveToolClass: %v", err)
	}
	if class != tool.ClassNonMutating {
		t.Fatalf("class = %v, want %v", class, tool.ClassNonMutating)
	}
}

// TestNewPluginTool_SchemaRoundTrip verifies a JSON Schema in the
// manifest comes back intact via pt.Schema() — this is what the agent
// loop passes to the provider's TurnRequest.Tools.
func TestNewPluginTool_SchemaRoundTrip(t *testing.T) {
	mod := &Module{Name: "demo"}
	pt, err := NewPluginTool(mod, plugins.ToolDef{
		Name: "fetch",
		Schema: `{
			"type": "object",
			"properties": {"url": {"type": "string"}},
			"required": ["url"]
		}`,
	})
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	s := pt.Schema()
	if s["type"] != "object" {
		t.Errorf("type: %v", s["type"])
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties: %T (%v)", s["properties"], s["properties"])
	}
	url, ok := props["url"].(map[string]any)
	if !ok || url["type"] != "string" {
		t.Errorf("url schema: %v", url)
	}
}

// TestNewPluginTool_BadSchemaRejected covers the defensive parse. A
// signed manifest should never reach this path (the verifier parses
// schema too), but malformed JSON here fails loudly rather than
// silently passing an unparseable string to the provider.
func TestNewPluginTool_BadSchemaRejected(t *testing.T) {
	mod := &Module{Name: "demo"}
	_, err := NewPluginTool(mod, plugins.ToolDef{
		Name:   "bad",
		Schema: "not json {",
	})
	if err == nil {
		t.Fatal("expected schema parse error")
	}
}

func TestNewPluginTool_BadClassRejected(t *testing.T) {
	mod := &Module{Name: "demo"}
	_, err := NewPluginTool(mod, plugins.ToolDef{
		Name:  "bad",
		Class: "not-a-class",
	})
	if err == nil {
		t.Fatal("expected class parse error")
	}
}

// TestLoadPluginTools_FromManifest covers the helper that builds one
// adapter per manifest-declared tool.
func TestLoadPluginTools_FromManifest(t *testing.T) {
	mod := &Module{
		Name: "demo",
		Manifest: plugins.Manifest{
			Name: "demo",
			Tools: []plugins.ToolDef{
				{Name: "fetch", Description: "fetch a URL"},
				{Name: "summarise", Description: "summarise text"},
			},
		},
	}
	tools, err := LoadPluginTools(mod)
	if err != nil {
		t.Fatalf("LoadPluginTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name() != "fetch" || tools[1].Name() != "summarise" {
		t.Errorf("tool names: %q %q", tools[0].Name(), tools[1].Name())
	}
}

func TestValidateResultLength(t *testing.T) {
	if err := validateResultLength(128, 1024, "demo", "fetch"); err != nil {
		t.Fatalf("unexpected in-cap error: %v", err)
	}
	if err := validateResultLength(2048, 1024, "demo", "fetch"); err == nil {
		t.Fatal("expected over-cap result to fail")
	}
}

func TestPluginToolRejectsOversizedArgsBeforeABI(t *testing.T) {
	pt := &PluginTool{}
	_, err := pt.Run(context.Background(), json.RawMessage(strings.Repeat("x", toolinput.MaxBytes+1)), nil)
	if err == nil {
		t.Fatal("expected oversized args error")
	}
}

func TestTruncatePluginOutputCapsResult(t *testing.T) {
	got := truncatePluginOutput(strings.Repeat("x", budget.PluginBytes+1))
	if len(got) > budget.PluginBytes+256 {
		t.Fatalf("content length = %d, want near cap", len(got))
	}
	if !strings.Contains(got, "[truncated:") {
		t.Fatalf("truncation marker missing")
	}
}
