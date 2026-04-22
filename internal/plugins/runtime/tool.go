package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/pkg/tool"
)

// PluginTool adapts a wasm-exported function to pkg/tool.Tool so the
// Executor can invoke it like any bundled tool.
//
// Plugin ABI convention (must be implemented on the plugin side):
//
//	stado_alloc(size i32) → ptr i32
//	    Return a writable offset in the plugin's linear memory. The
//	    host writes tool input here before invocation. Plugins usually
//	    back this with their language's standard heap allocator.
//
//	stado_free(ptr i32, size i32)
//	    Release memory allocated by stado_alloc. Called after the host
//	    has read the result (or in the error path).
//
//	stado_tool_<name>(argsPtr i32, argsLen i32, resultPtr i32, resultCap i32) → i32
//	    Run the tool. Plugin reads `argsLen` bytes of JSON at
//	    `argsPtr`, writes up to `resultCap` bytes of JSON result to
//	    `resultPtr`, returns the number of bytes written, or -1 on
//	    tool-side error.
//
// Why static name + schema? Both come from the plugin's signed
// manifest — they're part of the install-time contract, not runtime
// state. A plugin can't register new tools after install.
type PluginTool struct {
	mod    *Module
	def    plugins.ToolDef
	schema map[string]any
	class  tool.Class
}

// NewPluginTool builds one adapter per tool declared in a plugin's
// manifest. Returns an error for malformed schema JSON in the
// manifest; the install-time verifier should already have caught that,
// but a second check here keeps runtime-safe code defensive.
func NewPluginTool(mod *Module, def plugins.ToolDef) (*PluginTool, error) {
	var schema map[string]any
	if def.Schema != "" {
		if err := json.Unmarshal([]byte(def.Schema), &schema); err != nil {
			return nil, fmt.Errorf("plugin %s tool %s: schema JSON: %w",
				mod.Name, def.Name, err)
		}
	}
	class, err := EffectiveToolClass(def, mod.Manifest.Capabilities)
	if err != nil {
		return nil, err
	}
	return &PluginTool{mod: mod, def: def, schema: schema, class: class}, nil
}

// EffectiveToolClass computes the runtime class for one plugin tool.
//
// Tool classes in the manifest describe mutation intent only. Plugin
// capabilities are plugin-wide, so any declared tool can still use the plugin's
// broader host imports. A plugin with file-read, network, session, or LLM
// capabilities is therefore not "safe" just because one tool declared itself
// non-mutating. For safety-gating and audit we conservatively promote:
//   - fs:write:* -> at least Mutating
//   - fs:read:*, net:*, session:*, llm:* and unknown caps -> Exec
func EffectiveToolClass(def plugins.ToolDef, capabilities []string) (tool.Class, error) {
	class, err := def.ClassValue()
	if err != nil {
		return tool.ClassExec, err
	}
	for _, raw := range capabilities {
		cap := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case cap == "":
			continue
		case strings.HasPrefix(cap, "fs:write:"):
			class = maxClass(class, tool.ClassMutating)
		case strings.HasPrefix(cap, "exec:"):
			class = maxClass(class, tool.ClassExec)
		case strings.HasPrefix(cap, "fs:read:"),
			strings.HasPrefix(cap, "net:"),
			strings.HasPrefix(cap, "session:"),
			strings.HasPrefix(cap, "llm:"):
			class = maxClass(class, tool.ClassExec)
		case strings.HasPrefix(cap, "lsp:"):
			// lsp:* is read-only query capability. Leave the manifest
			// class intact unless another capability promotes it.
			continue
		default:
			class = maxClass(class, tool.ClassExec)
		}
	}
	return class, nil
}

func maxClass(a, b tool.Class) tool.Class {
	if b > a {
		return b
	}
	return a
}

func (p *PluginTool) Name() string        { return p.def.Name }
func (p *PluginTool) Description() string { return p.def.Description }
func (p *PluginTool) Schema() map[string]any {
	if p.schema == nil {
		return map[string]any{"type": "object"}
	}
	return p.schema
}

// Class delegates to the manifest-declared per-tool classification.
func (p *PluginTool) Class() tool.Class { return p.class }

// Run invokes the plugin tool via the wasm ABI described above.
//
// Flow:
//  1. alloc(len(args)) → argsPtr
//  2. mem.Write(argsPtr, args)
//  3. alloc(resultCap) → resultPtr
//  4. call stado_tool_<name>(argsPtr, argsLen, resultPtr, resultCap) → n
//  5. mem.Read(resultPtr, n) → result
//  6. free both buffers
//
// resultCap defaults to 1 MiB which comfortably covers the JSON size
// most tool responses reach. Can be made tunable later.
func (p *PluginTool) Run(ctx context.Context, args json.RawMessage, _ tool.Host) (tool.Result, error) {
	const resultCap = 1 << 20 // 1 MiB

	alloc := p.mod.wasmMod.ExportedFunction("stado_alloc")
	freeFn := p.mod.wasmMod.ExportedFunction("stado_free")
	exportName := "stado_tool_" + p.def.Name
	toolFn := p.mod.wasmMod.ExportedFunction(exportName)

	if alloc == nil || freeFn == nil || toolFn == nil {
		return tool.Result{
			Error: "plugin ABI incomplete: missing stado_alloc / stado_free / " + exportName,
		}, fmt.Errorf("plugin %s: missing ABI exports", p.mod.Name)
	}

	argsBytes := []byte(args)
	argsLen := uint32(len(argsBytes))

	// 1. Allocate args buffer.
	argsPtr, err := callAlloc(ctx, alloc, argsLen)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer callFree(ctx, freeFn, argsPtr, argsLen)

	// 2. Write args.
	if argsLen > 0 {
		if !p.mod.wasmMod.Memory().Write(argsPtr, argsBytes) {
			return tool.Result{Error: "wasm memory write failed"}, fmt.Errorf("wasm memory write")
		}
	}

	// 3. Allocate result buffer.
	resultPtr, err := callAlloc(ctx, alloc, resultCap)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer callFree(ctx, freeFn, resultPtr, resultCap)

	// 4. Invoke the tool function.
	ret, err := toolFn.Call(ctx,
		api.EncodeU32(argsPtr), api.EncodeU32(argsLen),
		api.EncodeU32(resultPtr), api.EncodeU32(resultCap))
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("plugin %s %s: %w",
			p.mod.Name, p.def.Name, err)
	}
	if len(ret) == 0 {
		return tool.Result{Error: "plugin returned no value"},
			fmt.Errorf("plugin %s %s: no return value", p.mod.Name, p.def.Name)
	}
	n := api.DecodeI32(ret[0])
	if n < 0 {
		msg, ok := readToolSideError(p.mod.wasmMod, resultPtr, n, resultCap)
		if ok {
			return tool.Result{Error: msg}, nil
		}
		return tool.Result{Error: fmt.Sprintf("plugin %s %s: tool-side error",
			p.mod.Name, p.def.Name)}, nil
	}
	if err := validateResultLength(n, resultCap, p.mod.Name, p.def.Name); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	// 5. Read result bytes.
	result, ok := p.mod.wasmMod.Memory().Read(resultPtr, uint32(n))
	if !ok {
		return tool.Result{Error: "wasm memory read failed"}, fmt.Errorf("wasm memory read")
	}
	// Defensive copy — wasm memory can be reused between calls.
	out := make([]byte, len(result))
	copy(out, result)
	return tool.Result{Content: string(out)}, nil
}

// LoadPluginTools walks the manifest's ToolDefs and builds an adapter
// per tool. The caller typically iterates + reg.Register()s each.
func LoadPluginTools(mod *Module) ([]*PluginTool, error) {
	out := make([]*PluginTool, 0, len(mod.Manifest.Tools))
	for _, td := range mod.Manifest.Tools {
		pt, err := NewPluginTool(mod, td)
		if err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, nil
}

func callAlloc(ctx context.Context, fn api.Function, size uint32) (uint32, error) {
	ret, err := fn.Call(ctx, api.EncodeU32(size))
	if err != nil {
		return 0, fmt.Errorf("stado_alloc: %w", err)
	}
	if len(ret) == 0 {
		return 0, fmt.Errorf("stado_alloc: no return value")
	}
	ptr := api.DecodeU32(ret[0])
	if ptr == 0 && size > 0 {
		return 0, fmt.Errorf("stado_alloc: returned null for size %d", size)
	}
	return ptr, nil
}

func callFree(ctx context.Context, fn api.Function, ptr, size uint32) {
	_, _ = fn.Call(ctx, api.EncodeU32(ptr), api.EncodeU32(size))
}

func validateResultLength(n, cap int32, pluginName, toolName string) error {
	if n > cap {
		return fmt.Errorf("plugin %s %s: result %d exceeds %d-byte cap",
			pluginName, toolName, n, cap)
	}
	return nil
}

func readToolSideError(mod api.Module, resultPtr uint32, n, cap int32) (string, bool) {
	if n >= 0 {
		return "", false
	}
	size := -n
	if size <= 0 || size > cap {
		return "", false
	}
	buf, ok := mod.Memory().Read(resultPtr, uint32(size))
	if !ok {
		return "", false
	}
	return string(buf), true
}

// Compile-time check: PluginTool satisfies pkg/tool.Tool.
var _ tool.Tool = (*PluginTool)(nil)
