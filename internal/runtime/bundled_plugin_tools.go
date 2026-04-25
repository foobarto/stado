package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/astgrep"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/lspfind"
	"github.com/foobarto/stado/internal/tools/readctx"
	"github.com/foobarto/stado/internal/tools/rg"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/internal/version"
	"github.com/foobarto/stado/pkg/tool"
)

// buildNativeRegistry keeps the current trusted host-wrapper
// implementations in Go. BuildDefaultRegistry wraps them in bundled wasm
// so the visible tool surface is plugin-backed while native code stays a
// thin host layer.
func buildNativeRegistry() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(fs.ReadTool{})
	r.Register(fs.WriteTool{})
	r.Register(fs.EditTool{})
	r.Register(fs.GlobTool{})
	r.Register(fs.GrepTool{})
	r.Register(bash.BashTool{Timeout: 60 * time.Second})
	r.Register(webfetch.WebFetchTool{})
	r.Register(rg.Tool{})
	r.Register(astgrep.Tool{})
	r.Register(readctx.Tool{})
	def := &lspfind.FindDefinition{}
	r.Register(def)
	r.Register(&lspfind.FindReferences{Definition: def})
	r.Register(&lspfind.DocumentSymbols{Definition: def})
	r.Register(&lspfind.Hover{Definition: def})
	return r
}

func buildBundledPluginRegistry() *tools.Registry {
	native := buildNativeRegistry()

	r := tools.NewRegistry()
	for _, t := range native.All() {
		r.Register(newBundledPluginTool(t, native.ClassOf(t.Name())))
	}
	r.Register(newBundledStaticTool(
		"approval_demo",
		"Manual test tool only. Do not use unless a human explicitly asks to test plugin approval UI.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
				"body":  map[string]any{"type": "string"},
			},
		},
		[]string{"ui:approval"},
	))
	// spawn_agent is native for now because it needs a live provider and
	// forked Session orchestration, not only the plugin host imports.
	r.Register(subagent.Tool{})
	return r
}

type bundledPluginTool struct {
	manifest plugins.Manifest
	def      plugins.ToolDef
	schema   map[string]any
	class    tool.Class
	wasm     []byte
}

func newBundledPluginTool(native tool.Tool, class tool.Class) tool.Tool {
	def := plugins.ToolDef{
		Name:        native.Name(),
		Description: native.Description(),
		Class:       pluginClassName(class),
		Schema:      mustMarshalSchema(native.Schema()),
	}
	var schema map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &schema)
	}
	return &bundledPluginTool{
		manifest: plugins.Manifest{
			Name:         bundledplugins.ManifestNamePrefix + "-" + native.Name(),
			Version:      version.Version,
			Author:       bundledplugins.Author,
			Capabilities: bundledToolCapabilities(native.Name()),
			Tools:        []plugins.ToolDef{def},
		},
		def:    def,
		schema: schema,
		class:  class,
		wasm:   bundledplugins.MustWasm(native.Name()),
	}
}

func newBundledStaticTool(name, desc string, class tool.Class, schema map[string]any, caps []string) tool.Tool {
	def := plugins.ToolDef{
		Name:        name,
		Description: desc,
		Class:       pluginClassName(class),
		Schema:      mustMarshalSchema(schema),
	}
	var parsed map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &parsed)
	}
	return &bundledPluginTool{
		manifest: plugins.Manifest{
			Name:         bundledplugins.ManifestNamePrefix + "-" + name,
			Version:      version.Version,
			Author:       bundledplugins.Author,
			Capabilities: caps,
			Tools:        []plugins.ToolDef{def},
		},
		def:    def,
		schema: parsed,
		class:  class,
		wasm:   bundledplugins.MustWasm(name),
	}
}

func (p *bundledPluginTool) Name() string        { return p.def.Name }
func (p *bundledPluginTool) Description() string { return p.def.Description }
func (p *bundledPluginTool) Schema() map[string]any {
	if p.schema == nil {
		return map[string]any{"type": "object"}
	}
	return p.schema
}
func (p *bundledPluginTool) Class() tool.Class { return p.class }

func (p *bundledPluginTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("bundled plugin %s: runtime: %w", p.def.Name, err)
	}
	defer func() { _ = rt.Close(ctx) }()

	host := pluginRuntime.NewHost(p.manifest, h.Workdir(), nil)
	host.ToolHost = h
	if bridge, ok := h.(pluginRuntime.ApprovalBridge); ok {
		host.ApprovalBridge = bridge
	}
	if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("bundled plugin %s: host imports: %w", p.def.Name, err)
	}
	mod, err := rt.Instantiate(ctx, p.wasm, p.manifest)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("bundled plugin %s: instantiate: %w", p.def.Name, err)
	}
	defer func() { _ = mod.Close(ctx) }()

	pt, err := pluginRuntime.NewPluginTool(mod, p.def)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return pt.Run(ctx, args, h)
}

func pluginClassName(class tool.Class) string {
	switch class {
	case tool.ClassStateMutating:
		return "StateMutating"
	case tool.ClassMutating:
		return "Mutating"
	case tool.ClassExec:
		return "Exec"
	default:
		return "NonMutating"
	}
}

func mustMarshalSchema(schema map[string]any) string {
	if len(schema) == 0 {
		return `{"type":"object"}`
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return `{"type":"object"}`
	}
	return string(raw)
}

func bundledToolCapabilities(name string) []string {
	switch name {
	case "read", "glob", "grep", "read_with_context":
		return []string{"fs:read:."}
	case "write":
		return []string{"fs:write:."}
	case "edit":
		return []string{"fs:read:.", "fs:write:."}
	case "bash":
		return []string{"exec:shallow_bash"}
	case "webfetch":
		return []string{"net:http_get"}
	case "ripgrep":
		return []string{"fs:read:.", "exec:search"}
	case "ast_grep":
		return []string{"fs:read:.", "fs:write:.", "exec:ast_grep"}
	case "find_definition", "find_references", "document_symbols", "hover":
		return []string{"fs:read:.", "lsp:query"}
	default:
		return nil
	}
}
