package runtime

import (
	"context"
	"encoding/json"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// wasmFamily describes one tool family's migration from native to wasm.
// nativeNames: the bare names registered by buildNativeRegistry.
// wasmAlias:   the local alias for the new plugin (e.g. "fs").
// wasmTools:   tool names within the plugin (e.g. ["read","write",...]).
type wasmFamily struct {
	nativeNames []string
	wasmAlias   string
	wasmTools   []wasmTool
}

type wasmTool struct {
	name        string   // tool name within plugin (e.g. "read")
	description string
	schema      map[string]any
	caps        []string
}

// wasmFamilies defines the migration map. Each entry is activated when
// cfg.Runtime.UseWasm[family] = true. EP-0038 §C.
var wasmFamilies = []struct {
	key    string // cfg.Runtime.UseWasm key
	family wasmFamily
}{
	{
		key: "fs",
		family: wasmFamily{
			nativeNames: []string{"read", "write", "edit", "glob", "grep"},
			wasmAlias:   "fs",
			wasmTools: []wasmTool{
				{
					name:        "read",
					description: "Read a file. Optional offset/length for partial reads.",
					schema: map[string]any{
						"type":     "object",
						"required": []string{"path"},
						"properties": map[string]any{
							"path":   map[string]any{"type": "string"},
							"offset": map[string]any{"type": "integer", "description": "Byte offset to start reading from."},
							"length": map[string]any{"type": "integer", "description": "Maximum bytes to read."},
						},
					},
					caps: []string{"fs:read:."},
				},
				{
					name:        "write",
					description: "Write content to a file (creates or truncates).",
					schema: map[string]any{
						"type": "object", "required": []string{"path", "content"},
						"properties": map[string]any{
							"path":    map[string]any{"type": "string"},
							"content": map[string]any{"type": "string"},
						},
					},
					caps: []string{"fs:write:."},
				},
				{
					name:        "edit",
					description: "Edit a file by replacing an exact string.",
					schema: map[string]any{
						"type": "object", "required": []string{"path", "old_string", "new_string"},
						"properties": map[string]any{
							"path":        map[string]any{"type": "string"},
							"old_string":  map[string]any{"type": "string"},
							"new_string":  map[string]any{"type": "string"},
							"replace_all": map[string]any{"type": "boolean"},
						},
					},
					caps: []string{"fs:read:.", "fs:write:."},
				},
				{
					name:        "glob",
					description: "List files matching a glob pattern.",
					schema: map[string]any{
						"type": "object", "required": []string{"pattern"},
						"properties": map[string]any{
							"pattern": map[string]any{"type": "string"},
						},
					},
					caps: []string{"fs:read:."},
				},
				{
					name:        "grep",
					description: "Search file contents with a regex pattern.",
					schema: map[string]any{
						"type": "object", "required": []string{"pattern"},
						"properties": map[string]any{
							"pattern":       map[string]any{"type": "string"},
							"path":          map[string]any{"type": "string"},
							"include":       map[string]any{"type": "string"},
							"case_insensitive": map[string]any{"type": "boolean"},
						},
					},
					caps: []string{"fs:read:."},
				},
			},
		},
	},
	{
		key: "shell",
		family: wasmFamily{
			nativeNames: []string{"bash"},
			wasmAlias:   "shell",
			wasmTools: []wasmTool{
				{
					name:        "exec",
					description: "Execute a shell command.",
					schema: map[string]any{
						"type": "object", "required": []string{"command"},
						"properties": map[string]any{
							"command":    map[string]any{"type": "string"},
							"timeout_ms": map[string]any{"type": "integer"},
						},
					},
					caps: []string{"exec:proc:/bin/sh"},
				},
			},
		},
	},
	{
		key: "rg",
		family: wasmFamily{
			nativeNames: []string{"ripgrep"},
			wasmAlias:   "rg",
			wasmTools: []wasmTool{
				{
					name:        "search",
					description: "Fast code search via ripgrep.",
					schema: map[string]any{
						"type": "object", "required": []string{"pattern"},
						"properties": map[string]any{
							"pattern": map[string]any{"type": "string"},
							"path":    map[string]any{"type": "string"},
							"flags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
					},
					caps: []string{"fs:read:.", "bundled-bin:ripgrep", "exec:proc"},
				},
			},
		},
	},
	{
		key: "astgrep",
		family: wasmFamily{
			nativeNames: []string{"ast_grep"},
			wasmAlias:   "astgrep",
			wasmTools: []wasmTool{
				{
					name:        "search",
					description: "Structural code search via ast-grep.",
					schema: map[string]any{
						"type": "object", "required": []string{"pattern"},
						"properties": map[string]any{
							"pattern":  map[string]any{"type": "string"},
							"language": map[string]any{"type": "string"},
							"path":     map[string]any{"type": "string"},
						},
					},
					caps: []string{"fs:read:.", "bundled-bin:ast-grep", "exec:proc"},
				},
			},
		},
	},
	{
		key: "readctx",
		family: wasmFamily{
			nativeNames: []string{"read_with_context"},
			wasmAlias:   "readctx",
			wasmTools: []wasmTool{
				{
					name:        "read",
					description: "Read a file with surrounding context lines.",
					schema: map[string]any{
						"type": "object", "required": []string{"path"},
						"properties": map[string]any{
							"path":    map[string]any{"type": "string"},
							"offset":  map[string]any{"type": "integer"},
							"limit":   map[string]any{"type": "integer"},
						},
					},
					caps: []string{"fs:read:."},
				},
			},
		},
	},
}

// ApplyWasmMigration swaps native tool registrations for wasm-backed ones
// for each family where cfg.Runtime.UseWasm[family] = true. EP-0038 §Migration.
func ApplyWasmMigration(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil || len(cfg.Runtime.UseWasm) == 0 {
		return
	}
	for _, entry := range wasmFamilies {
		if !cfg.Runtime.UseWasm[entry.key] {
			continue
		}
		fam := entry.family
		// Check the wasm binary is available before removing native tools.
		wasmBytes, err := bundledplugins.Wasm(fam.wasmAlias)
		if err != nil {
			continue // wasm not built yet — skip silently
		}
		// Register wasm-backed tools.
		for _, wt := range fam.wasmTools {
			wireName, err := tools.WireForm(fam.wasmAlias, wt.name)
			if err != nil {
				continue
			}
			reg.Register(newWasmMigrationTool(wireName, wt, wasmBytes, fam.wasmAlias, cfg))
		}
		// Remove native tools this family replaces.
		for _, native := range fam.nativeNames {
			reg.Unregister(native)
		}
	}
}

// wasmMigrationTool is a wasm-backed tool.Tool used during the EP-0038 migration.
type wasmMigrationTool struct {
	wireName  string
	wt        wasmTool
	wasmBytes []byte
	alias     string
	cfg       *config.Config
}

func newWasmMigrationTool(wireName string, wt wasmTool, wasmBytes []byte, alias string, cfg *config.Config) pkgtool.Tool {
	return &wasmMigrationTool{
		wireName:  wireName,
		wt:        wt,
		wasmBytes: wasmBytes,
		alias:     alias,
		cfg:       cfg,
	}
}

func (t *wasmMigrationTool) Name() string        { return t.wireName }
func (t *wasmMigrationTool) Description() string { return t.wt.description }
func (t *wasmMigrationTool) Schema() map[string]any {
	if t.wt.schema == nil {
		return map[string]any{"type": "object"}
	}
	return t.wt.schema
}

func (t *wasmMigrationTool) Run(ctx context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	manifest := plugins.Manifest{
		Name:         "stado-builtin-" + t.alias,
		Version:      "ep-0038",
		Author:       "stado",
		Capabilities: t.wt.caps,
	}
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return pkgtool.Result{Error: err.Error()}, err
	}
	defer func() { _ = rt.Close(ctx) }()

	host := pluginRuntime.NewHost(manifest, h.Workdir(), nil)
	host.ToolHost = h
	if bridge, ok := h.(pluginRuntime.ApprovalBridge); ok {
		host.ApprovalBridge = bridge
	}
	if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
		return pkgtool.Result{Error: err.Error()}, err
	}
	mod, err := rt.Instantiate(ctx, t.wasmBytes, manifest)
	if err != nil {
		return pkgtool.Result{Error: err.Error()}, err
	}
	defer func() { _ = mod.Close(ctx) }()

	toolDef := plugins.ToolDef{
		Name:        t.wt.name,
		Description: t.wt.description,
	}
	pt, err := pluginRuntime.NewPluginTool(mod, toolDef)
	if err != nil {
		return pkgtool.Result{Error: err.Error()}, err
	}
	return pt.Run(ctx, args, h)
}
