// stado_tool_invoke — wasm plugins call other registered tools. The
// tester's #3: "exploit_tomcat_war_deploy can't use exfil_listener_command
// to stand up a catch server before deploying the webshell. xxe_payload
// can't invoke cve_lookup to cross-reference the target service.
// Everything that should be plugin composition instead becomes agent-loop
// choreography that burns turns and context."
//
// Capability gate: tool:invoke[:<name-glob>]. Empty glob = match-all
// (broad). Per-name: tool:invoke:fs.read, tool:invoke:cve_lookup, etc.
//
// Recursion is bounded — max depth 4. Beyond that, _invoke returns -1
// to prevent infinite-loop runaway plugins.

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	toolInvokeMaxDepth = 4
)

// ToolInvokeAccess is the per-plugin capability + invocation surface
// for stado_tool_invoke. Populated when the manifest declares
// `tool:invoke[:<glob>]`.
type ToolInvokeAccess struct {
	// AllowedGlobs are the patterns the manifest declared. Empty = match-all.
	AllowedGlobs []string

	// Invoke is the host-supplied callback that runs a named tool with
	// JSON args and returns the JSON result. The host caller wires this
	// to the active session's tool registry. nil disables the import
	// (returns -1 for every call).
	Invoke func(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// CanInvoke returns true when name matches any of AllowedGlobs.
func (a *ToolInvokeAccess) CanInvoke(name string) bool {
	if a == nil {
		return false
	}
	if len(a.AllowedGlobs) == 0 {
		return true
	}
	for _, g := range a.AllowedGlobs {
		if matched, _ := filepath.Match(g, name); matched {
			return true
		}
	}
	return false
}

// invokeDepthKey is the private context-value key threading the
// current recursion depth through nested stado_tool_invoke calls.
type invokeDepthKey struct{}

// CurrentInvokeDepth reads the recursion depth from ctx (0 when not set).
// Used by host code wiring the Invoke callback to refuse calls deeper
// than toolInvokeMaxDepth.
func CurrentInvokeDepth(ctx context.Context) int {
	if v, ok := ctx.Value(invokeDepthKey{}).(*int32); ok {
		return int(atomic.LoadInt32(v))
	}
	return 0
}

// WithInvokeDepth returns a derived ctx that increments the recursion
// counter for any nested stado_tool_invoke calls.
func WithInvokeDepth(ctx context.Context, depth int) context.Context {
	d := int32(depth)
	return context.WithValue(ctx, invokeDepthKey{}, &d)
}

// registerToolInvokeImport wires the stado_tool_invoke host import.
func registerToolInvokeImport(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, namePtr, nameLen, argsPtr, argsLen, outPtr, outMax int32) int32 {
			if host.ToolInvoke == nil || host.ToolInvoke.Invoke == nil {
				return -1
			}
			depth := CurrentInvokeDepth(ctx)
			if depth >= toolInvokeMaxDepth {
				return -1
			}
			name, ok := readMemoryString(mod, uint32(namePtr), uint32(nameLen))
			if !ok {
				return -1
			}
			if !host.ToolInvoke.CanInvoke(name) {
				return -1
			}
			argsBytes, ok := mod.Memory().Read(uint32(argsPtr), uint32(argsLen))
			if !ok {
				return -1
			}
			argsCopy := make([]byte, len(argsBytes))
			copy(argsCopy, argsBytes)

			// Increment depth for any nested invocation.
			invokeCtx := WithInvokeDepth(ctx, depth+1)
			result, err := host.ToolInvoke.Invoke(invokeCtx, name, json.RawMessage(argsCopy))
			if err != nil {
				// Wrap the error in a JSON envelope so the plugin can
				// distinguish from a successful "tool returned an empty
				// result" — important since both paths write zero bytes.
				envelope, _ := json.Marshal(map[string]any{"error": err.Error()})
				if int32(len(envelope)) > outMax {
					return int32(len(envelope))
				}
				if !mod.Memory().Write(uint32(outPtr), envelope) {
					return -1
				}
				return int32(len(envelope))
			}
			n := int32(len(result))
			if n > outMax {
				return n // tells plugin to re-call with larger buffer
			}
			if !mod.Memory().Write(uint32(outPtr), []byte(result)) {
				return -1
			}
			return n
		}).
		Export("stado_tool_invoke")
}

// Sanity stub keeps the file importable if the build excludes
// stado_tool_invoke later.
var _ = fmt.Sprintf
