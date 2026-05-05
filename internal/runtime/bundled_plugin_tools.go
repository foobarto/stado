package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/toolinput"
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
	// EP-0038c: fs.ls — bundled into the fs wasm module (uses stado_exec for /bin/ls).
	r.Register(newBundledWasmTool("fs", "stado_tool_ls", "fs__ls",
		"List a directory with structured metadata: name, type (file/dir/symlink), size, permissions, mtime. Returns the formatted ls output.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Directory to list (default '.')"},
				"hidden": map[string]any{"type": "boolean", "description": "Include dot-files (default false)"},
			},
		},
		[]string{"exec:proc:/bin/ls", "exec:proc:/usr/bin/ls", "fs:read:."}))

	// EP-0038c: shell.* PTY session tools — wasm-backed via shell.wasm.
	// Capabilities: terminal:open (PTY) + exec:proc (one-shot variants).
	shellSessionCaps := []string{"terminal:open", "exec:proc"}
	r.Register(newBundledWasmTool("shell", "stado_tool_spawn", "shell__spawn",
		"Open an interactive PTY shell session. Returns {id} — use shell.read / shell.write / shell.destroy to drive it. Persists across tool calls. Args: argv? (default ['/bin/bash']), env?, cwd?, cols?, rows?, buffer_bytes?",
		tool.ClassExec,
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"env":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"cwd":  map[string]any{"type": "string"},
				"cols": map[string]any{"type": "integer"}, "rows": map[string]any{"type": "integer"},
			},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_list", "shell__list",
		"List active PTY shell sessions: id, cmd, alive, attached, started_at, buffered, dropped, exit_code.",
		tool.ClassNonMutating,
		map[string]any{"type": "object"},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_attach", "shell__attach",
		"Attach to a PTY session to read/write. Single-attach lock per session — use force:true to steal. Args: id, force?",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{
				"id":    map[string]any{"type": "integer"},
				"force": map[string]any{"type": "boolean"},
			},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_detach", "shell__detach",
		"Release the attachment lock on a PTY session. Args: id.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{"id": map[string]any{"type": "integer"}},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_write", "shell__write",
		"Write input to a PTY session's stdin. Args: id, data (UTF-8 string) OR data_b64 (raw bytes). Requires attach.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{
				"id":       map[string]any{"type": "integer"},
				"data":     map[string]any{"type": "string"},
				"data_b64": map[string]any{"type": "string"},
			},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_read", "shell__read",
		"Read buffered output from a PTY session. Args: id, max_bytes?, timeout_ms?. Returns {data?, data_b64, n, eof?}. Requires attach.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{
				"id":         map[string]any{"type": "integer"},
				"max_bytes":  map[string]any{"type": "integer"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_signal", "shell__signal",
		"Send a POSIX signal to a PTY session. Args: id, sig (e.g. 'SIGINT', 'SIGTERM', 9). Out-of-band — no attach required.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id", "sig"},
			"properties": map[string]any{
				"id":  map[string]any{"type": "integer"},
				"sig": map[string]any{},
			},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_resize", "shell__resize",
		"Resize a PTY session. Args: id, cols, rows. Out-of-band — no attach required.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id", "cols", "rows"},
			"properties": map[string]any{
				"id":   map[string]any{"type": "integer"},
				"cols": map[string]any{"type": "integer"},
				"rows": map[string]any{"type": "integer"},
			},
		},
		shellSessionCaps))
	r.Register(newBundledWasmTool("shell", "stado_tool_destroy", "shell__destroy",
		"Kill a PTY session and free its resources. Args: id.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{"id": map[string]any{"type": "integer"}},
		},
		shellSessionCaps))

	// EP-0038c: agent.* tools — wasm-backed via agent.wasm + FleetBridge.
	agentCaps := []string{"agent:fleet"}
	r.Register(newBundledWasmTool("agent", "stado_tool_spawn", "agent__spawn",
		"Spawn a sub-agent. Returns {id, session_id, status, final_text?}. Default async=false blocks until child completes; async=true returns immediately. Default model inherits parent's. EP-0038 §D.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"prompt"},
			"properties": map[string]any{
				"prompt":          map[string]any{"type": "string"},
				"model":           map[string]any{"type": "string"},
				"async":           map[string]any{"type": "boolean"},
				"ephemeral":       map[string]any{"type": "boolean"},
				"parent_session":  map[string]any{"type": "string"},
				"sandbox_profile": map[string]any{"type": "string"},
				"allowed_tools":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
		agentCaps))
	r.Register(newBundledWasmTool("agent", "stado_tool_list", "agent__list",
		"List agents in caller's spawn tree. Returns [{id, session_id, status, model, started_at, last_turn_at, cost_so_far_usd}].",
		tool.ClassNonMutating,
		map[string]any{"type": "object"},
		agentCaps))
	r.Register(newBundledWasmTool("agent", "stado_tool_read_messages", "agent__read_messages",
		"Read assistant-role messages from an agent's output channel. Optional since/timeout for incremental polling. Returns {messages, offset, status}.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{
				"id":         map[string]any{"type": "string"},
				"since":      map[string]any{"type": "integer"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
		},
		agentCaps))
	r.Register(newBundledWasmTool("agent", "stado_tool_send_message", "agent__send_message",
		"Send a user-role message into an agent's inbox. Delivered at the agent's next yield point.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id", "message"},
			"properties": map[string]any{
				"id":      map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
			},
		},
		agentCaps))
	// EP-0038c: web.* and dns.* — wasm-backed wrappers over existing host imports.
	r.Register(newBundledWasmTool("web", "stado_tool_fetch", "web__fetch",
		"Fetch a URL and return the body converted to markdown. Supports HTTPS to public hosts via net:http_request capability.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"url"},
			"properties": map[string]any{
				"url":        map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
		},
		[]string{"net:http_request"}))
	r.Register(newBundledWasmTool("dns", "stado_tool_resolve", "dns__resolve",
		"DNS lookup: A/AAAA (default), TXT, MX, NS, PTR. Args: name, qtype?, server?, timeout_ms?. Returns {records, error?}.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"name"},
			"properties": map[string]any{
				"name":       map[string]any{"type": "string"},
				"qtype":      map[string]any{"type": "string", "enum": []string{"A", "AAAA", "TXT", "MX", "NS", "PTR"}},
				"server":     map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
		},
		[]string{"dns:resolve"}))

	r.Register(newBundledWasmTool("agent", "stado_tool_cancel", "agent__cancel",
		"Cancel a running agent. The child exits at its next yield point.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"id"},
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
		},
		agentCaps))
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

// newBundledWasmTool registers one tool from a multi-tool wasm module.
// wasmName: the .wasm file basename in internal/bundledplugins/wasm/.
// toolExport: the wasm export name; either the full "stado_tool_<X>" form
// or the bare "<X>" suffix — both work, the prefix is stripped here.
// The dispatcher in internal/plugins/runtime/tool.go:Run prepends
// "stado_tool_" to def.Name to resolve the export, so def.Name needs to
// be the bare suffix. registeredName is how the tool is named in the
// registry (typically wire form, e.g. "fs__ls").
func newBundledWasmTool(wasmName, toolExport, registeredName, desc string, class tool.Class, schema map[string]any, caps []string) tool.Tool {
	bare := strings.TrimPrefix(toolExport, "stado_tool_")
	def := plugins.ToolDef{
		Name:        bare,
		Description: desc,
		Class:       pluginClassName(class),
		Schema:      mustMarshalSchema(schema),
	}
	var parsed map[string]any
	if def.Schema != "" {
		_ = json.Unmarshal([]byte(def.Schema), &parsed)
	}
	t := &bundledPluginTool{
		manifest: plugins.Manifest{
			Name:         bundledplugins.ManifestNamePrefix + "-" + wasmName,
			Version:      version.Version,
			Author:       bundledplugins.Author,
			Capabilities: caps,
			Tools:        []plugins.ToolDef{def},
		},
		def:    def,
		schema: parsed,
		class:  class,
		wasm:   bundledplugins.MustWasm(wasmName),
	}
	// Override the visible name for the registry (wire form).
	return &renamedTool{inner: t, name: registeredName}
}

// renamedTool wraps a tool to expose a different Name() — the inner tool
// still calls its underlying wasm export, but the registry sees the wire name.
type renamedTool struct {
	inner tool.Tool
	name  string
}

func (r *renamedTool) Name() string        { return r.name }
func (r *renamedTool) Description() string { return r.inner.Description() }
func (r *renamedTool) Schema() map[string]any {
	return r.inner.Schema()
}
func (r *renamedTool) Class() tool.Class {
	if c, ok := r.inner.(tool.Classifier); ok {
		return c.Class()
	}
	return tool.ClassExec
}
func (r *renamedTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	return r.inner.Run(ctx, args, h)
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
	if err := toolinput.CheckLen(len(args)); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("bundled plugin %s: runtime: %w", p.def.Name, err)
	}
	defer func() { _ = rt.Close(ctx) }()

	host := pluginRuntime.NewHost(p.manifest, h.Workdir(), nil)
	host.ToolHost = h
	// EP-0038c: wire FleetBridge for agent.* tools when the host provides one.
	if afp, ok := h.(tool.AgentFleetProvider); ok {
		if fb, ok := afp.AgentFleetBridge().(pluginRuntime.FleetBridge); ok {
			host.FleetBridge = fb
		}
	}
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
