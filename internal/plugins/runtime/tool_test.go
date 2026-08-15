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
		Name:         "fetch",
		Description:  "fetch a URL",
		Capabilities: plugins.CapabilitySubset(),
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

func TestDecodeToolSideErrorEnvelope(t *testing.T) {
	code := 127
	payload, err := json.Marshal(tool.ErrorEnvelopeV1{
		Schema: tool.ErrorEnvelopeSchemaV1, Kind: tool.FailureExit,
		Message: "command exited with code 127", ExitCode: &code,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolSideError(string(payload))
	if got.Error != "command exited with code 127" || got.FailureKind != tool.FailureExit ||
		got.ExitCode == nil || *got.ExitCode != 127 {
		t.Fatalf("decoded result = %+v", got)
	}

	plain := decodeToolSideError("ordinary plugin failure")
	if plain.Error != "ordinary plugin failure" || plain.FailureKind != tool.FailureUnknown || plain.ExitCode != nil {
		t.Fatalf("plain result = %+v", plain)
	}
}

func TestNewPluginTool_ClassRoundTrip(t *testing.T) {
	mod := &Module{Name: "demo"}
	pt, err := NewPluginTool(mod, plugins.ToolDef{
		Name:         "execy",
		Class:        "Exec",
		Capabilities: plugins.CapabilitySubset(),
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
	pt, err := NewPluginTool(mod, plugins.ToolDef{Name: "inspect", Class: "NonMutating", Capabilities: plugins.CapabilitySubset("fs:read:/work")})
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
	pt, err := NewPluginTool(mod, plugins.ToolDef{Name: "inspect", Class: "NonMutating", Capabilities: plugins.CapabilitySubset("fs:read:.", "lsp:query")})
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

func TestEffectiveToolClass_RegistryCatalogAndSessionSurface(t *testing.T) {
	search, err := EffectiveToolClass(plugins.ToolDef{Name: "search", Class: "NonMutating"}, []string{"registry:catalog"})
	if err != nil {
		t.Fatalf("catalog class: %v", err)
	}
	if search != tool.ClassNonMutating {
		t.Fatalf("catalog search class = %v, want non-mutating", search)
	}

	activate, err := EffectiveToolClass(plugins.ToolDef{Name: "activate", Class: "NonMutating"}, []string{"registry:catalog", "session:tool-surface"})
	if err != nil {
		t.Fatalf("surface class: %v", err)
	}
	if activate != tool.ClassStateMutating {
		t.Fatalf("surface edit class = %v, want state-mutating", activate)
	}
}

func TestNewPluginToolUsesPerToolCapabilitiesForClassification(t *testing.T) {
	manifest := plugins.Manifest{
		Capabilities: []string{"registry:catalog", "session:tool-surface"},
	}
	search := plugins.ToolDef{Name: "tools__search", Class: "NonMutating", Capabilities: plugins.CapabilitySubset("registry:catalog")}
	activate := plugins.ToolDef{Name: "tools__activate", Class: "StateMutating", Capabilities: plugins.CapabilitySubset("registry:catalog", "session:tool-surface")}
	mod := &Module{Name: "tool-registry", Manifest: manifest}
	searchTool, err := NewPluginTool(mod, search)
	if err != nil {
		t.Fatal(err)
	}
	if searchTool.Class() != tool.ClassNonMutating {
		t.Fatalf("search class = %v", searchTool.Class())
	}
	activateTool, err := NewPluginTool(mod, activate)
	if err != nil {
		t.Fatal(err)
	}
	if activateTool.Class() != tool.ClassStateMutating {
		t.Fatalf("activate class = %v", activateTool.Class())
	}
}

func TestEffectiveToolClass_ProviderInvokeIsStateMutating(t *testing.T) {
	class, err := EffectiveToolClass(plugins.ToolDef{Name: "invoke", Class: "StateMutating"}, []string{"provider:invoke:16384"})
	if err != nil {
		t.Fatal(err)
	}
	if class != tool.ClassStateMutating {
		t.Fatalf("provider tool class = %v, want state-mutating", class)
	}
	unknown, err := EffectiveToolClass(plugins.ToolDef{Name: "future", Class: "NonMutating"}, []string{"provider:future"})
	if err != nil {
		t.Fatal(err)
	}
	if unknown != tool.ClassExec {
		t.Fatalf("unknown provider capability class = %v, want exec", unknown)
	}
}

func TestEffectiveToolClass_ResearchCapabilitiesStayWithinReadAndStateClasses(t *testing.T) {
	inner, err := EffectiveToolClass(plugins.ToolDef{Name: "research__open", Class: "NonMutating"}, []string{"evidence:open:artifact"})
	if err != nil || inner != tool.ClassNonMutating {
		t.Fatalf("evidence helper class=%v err=%v, want non-mutating", inner, err)
	}
	outer, err := EffectiveToolClass(plugins.ToolDef{Name: "memory__research", Class: "StateMutating"}, []string{"agent:spawn", "evidence:validate"})
	if err != nil || outer != tool.ClassStateMutating {
		t.Fatalf("research orchestrator class=%v err=%v, want state-mutating", outer, err)
	}
}

// TestNewPluginTool_SchemaRoundTrip verifies a JSON Schema in the
// manifest comes back intact via pt.Schema() — this is what the agent
// loop passes to the provider's TurnRequest.Tools.
func TestNewPluginTool_SchemaRoundTrip(t *testing.T) {
	mod := &Module{Name: "demo"}
	pt, err := NewPluginTool(mod, plugins.ToolDef{
		Name:         "fetch",
		Capabilities: plugins.CapabilitySubset(),
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
		Name:         "bad",
		Schema:       "not json {",
		Capabilities: plugins.CapabilitySubset(),
	})
	if err == nil {
		t.Fatal("expected schema parse error")
	}
}

func TestNewPluginTool_BadClassRejected(t *testing.T) {
	mod := &Module{Name: "demo"}
	_, err := NewPluginTool(mod, plugins.ToolDef{
		Name:         "bad",
		Class:        "not-a-class",
		Capabilities: plugins.CapabilitySubset(),
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
				{Name: "fetch", Description: "fetch a URL", Capabilities: plugins.CapabilitySubset()},
				{Name: "summarise", Description: "summarise text", Capabilities: plugins.CapabilitySubset()},
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
