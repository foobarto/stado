package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime/pluginrun"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/version"
	"github.com/foobarto/stado/pkg/tool"
)

// buildNativeRegistry keeps the current trusted host-wrapper
// implementations in Go. BuildDefaultRegistry wraps them in bundled wasm
// so the visible tool surface is plugin-backed while native code stays a
// thin host layer.
func buildNativeRegistry() *tools.Registry {
	r := tools.NewRegistry()
	// fs native registrations removed Step 7 of EP-no-internal-tools —
	// replaced by wasm fs__read / fs__write / fs__edit / fs__glob /
	// fs__grep registered via newBundledWasmTool below.
	// bash native registration removed Step 4 of EP-no-internal-tools —
	// replaced by the wasm shell__bash / shell__exec / shell__sh / shell__zsh
	// tools registered below using stado_exec primitives.
	// webfetch native registration removed Step 2 of EP-no-internal-tools
	// — replaced by the wasm web__fetch tool registered below using the
	// stado_http_request primitive.
	// rg + astgrep native registrations removed Step 5 of EP-no-internal-
	// tools — replaced by wasm rg__search / astgrep__search registered
	// below (which use stado_exec to spawn the binaries).
	// readctx native registration removed Step 7 of EP-no-internal-tools —
	// replaced by fs.read_with_context registered via newBundledWasmTool.
	// lspfind native registrations removed Step 6 of EP-no-internal-tools —
	// the four lsp wasm shims (find_definition / find_references /
	// document_symbols / hover) call the now-primitive stado_lsp_*
	// host imports directly. Native code stays in internal/lspfind/
	// as the host-side LSP client cache + protocol wrapping.
	return r
}

func buildBundledPluginRegistry() *tools.Registry {
	native := buildNativeRegistry()

	r := tools.NewRegistry()
	for _, t := range native.All() {
		r.Register(newBundledPluginTool(t, native.ClassOf(t.Name())))
	}
	// approval_demo and choose_demo were previously bundled as static
	// tools to manually exercise the ui:approval / ui:choice primitives.
	// They are now shipped as plugins/examples/{approval-demo-go,
	// choose-demo-go} — installed manually via `stado plugin install`.
	// Demos shouldn't live in the bundled tool surface (the model can
	// see them otherwise) and the example layout is the project's
	// canonical home for "implementation references."
	// EP-no-internal-tools Step 7: fs.* tools — wasm-backed via the fs
	// wasm plugin's stado_fs_* primitives. Replaces the native fs.ReadTool /
	// fs.WriteTool / fs.EditTool / fs.GlobTool / fs.GrepTool registrations.
	fsReadSchema := map[string]any{
		"type": "object", "required": []string{"path"},
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer", "description": "Byte offset (default 0)"},
			"length": map[string]any{"type": "integer", "description": "Max bytes to read"},
		},
	}
	r.Register(newBundledWasmTool("fs", "stado_tool_read", "fs__read",
		"Read a file. Optional offset/length for partial reads.",
		tool.ClassNonMutating, fsReadSchema, []string{"fs:read:."}))
	r.Register(newBundledWasmTool("fs", "stado_tool_write", "fs__write",
		"Write content to a file (creates or truncates).",
		tool.ClassMutating,
		map[string]any{
			"type": "object", "required": []string{"path", "content"},
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
		},
		[]string{"fs:write:."}))
	r.Register(newBundledWasmTool("fs", "stado_tool_edit", "fs__edit",
		"Edit a file by replacing an exact string. Set replace_all=true for multi-occurrence.",
		tool.ClassMutating,
		map[string]any{
			"type": "object", "required": []string{"path", "old_string", "new_string"},
			"properties": map[string]any{
				"path":        map[string]any{"type": "string"},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
		},
		[]string{"fs:read:.", "fs:write:."}))
	r.Register(newBundledWasmTool("fs", "stado_tool_glob", "fs__glob",
		"Find files matching a glob pattern. Walks recursively from path (default cwd).",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"pattern"},
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern (e.g. *.go)"},
				"path":    map[string]any{"type": "string", "description": "Walk root (default '.')"},
			},
		},
		[]string{"fs:read:."}))
	r.Register(newBundledWasmTool("fs", "stado_tool_grep", "fs__grep",
		"Search file contents with regex. Walks recursively from path (default cwd).",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"pattern"},
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Regex pattern"},
				"path":    map[string]any{"type": "string", "description": "Walk root (default '.')"},
			},
		},
		[]string{"fs:read:."}))
	r.Register(newBundledWasmTool("fs", "stado_tool_read_context", "readctx__read",
		"Read a file with a window of context around a target line.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"path"},
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"line":  map[string]any{"type": "integer", "description": "Center line (1-indexed)"},
				"range": map[string]any{"type": "integer", "description": "Total lines to show (default 20)"},
			},
		},
		[]string{"fs:read:."}))

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

	// EP-no-internal-tools Step 4: shell.exec / shell.bash / shell.sh /
	// shell.zsh — one-shot exec via stado_exec. Replaces the native
	// bash.BashTool (registered as bare `bash`, displayed as shell.exec).
	// Each scoped to its specific binary via exec:proc:<basename>.
	commandSchema := map[string]any{
		"type": "object", "required": []string{"command"},
		"properties": map[string]any{
			"command":    map[string]any{"type": "string", "description": "Shell command to run"},
			"timeout_ms": map[string]any{"type": "integer", "description": "Timeout in milliseconds (default 30000)"},
		},
	}
	r.Register(newBundledWasmTool("shell", "stado_tool_exec", "shell__exec",
		"Execute a shell command via /bin/sh -c, return combined stdout+stderr.",
		tool.ClassExec, commandSchema,
		[]string{"exec:proc:sh"}))
	r.Register(newBundledWasmTool("shell", "stado_tool_bash", "shell__bash",
		"Execute a shell command via /bin/bash -c, return combined stdout+stderr.",
		tool.ClassExec, commandSchema,
		[]string{"exec:proc:bash"}))
	r.Register(newBundledWasmTool("shell", "stado_tool_sh", "shell__sh",
		"Execute a shell command via /bin/sh -c, return combined stdout+stderr.",
		tool.ClassExec, commandSchema,
		[]string{"exec:proc:sh"}))
	r.Register(newBundledWasmTool("shell", "stado_tool_zsh", "shell__zsh",
		"Execute a shell command via /usr/bin/zsh -c, return combined stdout+stderr.",
		tool.ClassExec, commandSchema,
		[]string{"exec:proc:zsh"}))

	// EP-no-internal-tools Step 5: rg.search + astgrep.search via
	// stado_exec spawning the bundled binaries. Replaces the native
	// rg.Tool / astgrep.Tool registrations.
	rgSchema := map[string]any{
		"type": "object", "required": []string{"pattern"},
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Regex pattern"},
			"path":    map[string]any{"type": "string", "description": "Search root (default cwd)"},
			"flags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Extra rg flags (e.g. ['--hidden','-i'])"},
		},
	}
	r.Register(newBundledWasmTool("rg", "stado_tool_search", "rg__search",
		"Fast file-contents search via ripgrep. Pass pattern + optional path + optional flags.",
		tool.ClassNonMutating, rgSchema,
		[]string{"fs:read:.", "exec:proc:rg", "bundled-bin:rg"}))

	astgrepSchema := map[string]any{
		"type": "object", "required": []string{"pattern"},
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "ast-grep pattern, e.g. 'fmt.Println($X)'"},
			"lang":    map[string]any{"type": "string", "description": "Language (e.g. 'go', 'python', 'js')"},
			"path":    map[string]any{"type": "string", "description": "Search root (default cwd)"},
			"rewrite": map[string]any{"type": "string", "description": "Rewrite template; when set, files are updated in place"},
		},
	}
	r.Register(newBundledWasmTool("astgrep", "stado_tool_search", "astgrep__search",
		"Structural code search and rewrite via ast-grep (tree-sitter patterns).",
		tool.ClassMutating, astgrepSchema,
		[]string{"fs:read:.", "fs:write:.", "exec:proc:ast-grep", "bundled-bin:astgrep"}))

	// EP-no-internal-tools Step 6: lsp.* tools — wasm shims forwarding to
	// stado_lsp_* (now true primitives, no longer delegates to native
	// lspfind.Tool structs). Each is its own wasm module (find_definition.wasm,
	// etc.).
	lspPositionalSchema := map[string]any{
		"type": "object", "required": []string{"path", "line", "column"},
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"line":   map[string]any{"type": "integer", "description": "1-indexed line"},
			"column": map[string]any{"type": "integer", "description": "1-indexed column"},
		},
	}
	lspCaps := []string{"fs:read:.", "lsp:query"}
	r.Register(newBundledWasmTool("find_definition", "stado_tool_definition", "lsp__definition",
		"LSP textDocument/definition — jump to the declaration of a symbol at path:line:column.",
		tool.ClassNonMutating, lspPositionalSchema, lspCaps))
	r.Register(newBundledWasmTool("hover", "stado_tool_hover", "lsp__hover",
		"LSP textDocument/hover — docs/type for a symbol at path:line:column.",
		tool.ClassNonMutating, lspPositionalSchema, lspCaps))

	lspRefsSchema := map[string]any{
		"type": "object", "required": []string{"path", "line", "column"},
		"properties": map[string]any{
			"path":                map[string]any{"type": "string"},
			"line":                map[string]any{"type": "integer", "description": "1-indexed line"},
			"column":              map[string]any{"type": "integer", "description": "1-indexed column"},
			"include_declaration": map[string]any{"type": "boolean", "description": "default true"},
		},
	}
	r.Register(newBundledWasmTool("find_references", "stado_tool_references", "lsp__references",
		"LSP textDocument/references — every usage of a symbol.",
		tool.ClassNonMutating, lspRefsSchema, lspCaps))

	lspSymbolsSchema := map[string]any{
		"type": "object", "required": []string{"path"},
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	}
	r.Register(newBundledWasmTool("document_symbols", "stado_tool_symbols", "lsp__symbols",
		"LSP textDocument/documentSymbol — file outline (functions, types, methods).",
		tool.ClassNonMutating, lspSymbolsSchema, lspCaps))

	// EP-0038c: agent.* tools — wasm-backed via agent.wasm + FleetBridge.
	agentCaps := []string{"agent:fleet"}
	r.Register(newBundledWasmTool("agent", "stado_tool_spawn", "agent__spawn",
		"Spawn a sub-agent. Returns {id, session_id, status, final_text?}. Default async=false blocks until child completes; async=true returns immediately. Default model inherits parent's. Default persona inherits parent's. EP-0038 §D.",
		tool.ClassExec,
		map[string]any{
			"type": "object", "required": []string{"prompt"},
			"properties": map[string]any{
				"prompt":          map[string]any{"type": "string"},
				"model":           map[string]any{"type": "string"},
				"persona":         map[string]any{"type": "string", "description": "Persona for the child (operating manual). Empty = inherit parent's persona."},
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

	// 2026-05-06: session.search — bundled wasm plugin that uses
	// session:read to fetch the conversation history then runs a
	// substring or regex search in-wasm. Cap-gated by session:read.
	r.Register(newBundledWasmTool("session_search", "stado_tool_session_search", "session__search",
		"Search the current session's message history for a substring (default) or regex (is_regex=true). Returns matched messages with role, index, and a context snippet. Useful for recalling earlier discussion in long sessions without rebuilding context manually.",
		tool.ClassNonMutating,
		map[string]any{
			"type": "object", "required": []string{"query"},
			"properties": map[string]any{
				"query":            map[string]any{"type": "string", "description": "Substring or regex to search for."},
				"is_regex":         map[string]any{"type": "boolean", "description": "Treat query as a Go RE2 regex (default false = substring)."},
				"case_sensitive":   map[string]any{"type": "boolean", "description": "Case-sensitive matching (default false = case-insensitive)."},
				"roles":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Restrict to specific roles (user, assistant, tool, tool_result, system). Default: all roles."},
				"max_results":      map[string]any{"type": "integer", "description": "Cap on returned matches (default 50, max 1000)."},
				"snippet_chars":    map[string]any{"type": "integer", "description": "Total chars of context around each match (default 80, max 400)."},
			},
		},
		[]string{"session:read"}))

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
	caps := bundledToolCapabilities(native.Name())
	t := &bundledPluginTool{
		manifest: plugins.Manifest{
			Name:         bundledplugins.ManifestNamePrefix + "-" + native.Name(),
			Version:      version.Version,
			Author:       bundledplugins.Author,
			Capabilities: caps,
			Tools:        []plugins.ToolDef{def},
		},
		def:    def,
		schema: schema,
		class:  class,
		wasm:   bundledplugins.MustWasm(native.Name()),
	}
	bundledplugins.RegisterModule(native.Name(), native.Name(), caps)
	return t
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
	t := &bundledPluginTool{
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
	bundledplugins.RegisterModule(name, name, caps)
	return t
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
	bundledplugins.RegisterModule(wasmName, registeredName, caps)
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

// PluginName forwards to the inner wrapper so registry consumers can
// group tools by plugin without unwrapping the renamedTool layer.
func (r *renamedTool) PluginName() string {
	if pn, ok := r.inner.(interface{ PluginName() string }); ok {
		return pn.PluginName()
	}
	return ""
}

// PluginName returns the manifest name the bundled plugin tool was
// registered under (e.g. "stado-builtin-tool-fs"). Implements the
// runtime.pluginNamer interface — used by AutoloadedPluginNames to
// group autoloaded tools by source module on the TUI landing screen.
func (p *bundledPluginTool) PluginName() string { return p.manifest.Name }

func (p *bundledPluginTool) Name() string        { return p.def.Name }
func (p *bundledPluginTool) Description() string { return p.def.Description }
func (p *bundledPluginTool) Schema() map[string]any {
	if p.schema == nil {
		return map[string]any{"type": "object"}
	}
	return p.schema
}
func (p *bundledPluginTool) Class() tool.Class { return p.class }

// Run dispatches the bundled plugin via pluginrun.Run. Pre-Step-0.2
// this function had its own copy of the runtime + host setup +
// lifecycle wiring; pluginrun absorbed all of it. Bundled and installed
// plugins now share the same invocation primitive — the only
// difference is where the wasm bytes come from (embed.FS for bundled,
// disk for installed).
func (p *bundledPluginTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	if err := toolinput.CheckLen(len(args)); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return pluginrun.Run(ctx, pluginrun.RunArgs{
		Manifest:       p.manifest,
		WasmBytes:      p.wasm,
		ToolName:       p.def.Name,
		Args:           args,
		Cfg:            installedRunCfg, // bound at registry-build time
		Workdir:        h.Workdir(),
		InvokeRegistry: installedInvokeReg,
	}, h)
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
