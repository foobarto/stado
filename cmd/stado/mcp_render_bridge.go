package main

import (
	"context"
	"strings"
	"sync"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tui"
)

// mcpHostWithRender wraps the base stadoMCPHost with a per-call
// render bridge that accumulates panels emitted during a single
// tool dispatch. After tool.Run returns, the handler drains the
// bridge and packs the panels into the MCP CallToolResult.
//
// One bridge per call (not per server) so concurrent tool dispatches
// don't cross-talk. The base host stays shared because everything
// else on it (workdir, runner, PTY manager) IS server-lifetime by
// design.
//
// Spec: F9b.4 (.agent/specs/open/f9b-ui-render.md).
type mcpHostWithRender struct {
	stadoMCPHost
	bridge *mcpRenderBridge
}

// Render satisfies pluginRuntime.RenderBridge so pluginrun's
// attachLifecycleBridges picks up the per-call bridge via interface
// assertion. Fire-and-forget per spec — appends and returns.
func (h mcpHostWithRender) Render(ctx context.Context, panel pluginRuntime.Panel) error {
	return h.bridge.Render(ctx, panel)
}

// mcpRenderBridge buffers panels emitted within one MCP tool call.
// Mutex-guarded because plugins are free to fan out goroutines
// inside a tool dispatch (rare in practice but the cost of
// guarding is one mutex op per emit).
type mcpRenderBridge struct {
	mu     sync.Mutex
	panels []pluginRuntime.Panel
}

func (b *mcpRenderBridge) Render(_ context.Context, panel pluginRuntime.Panel) error {
	b.mu.Lock()
	b.panels = append(b.panels, panel)
	b.mu.Unlock()
	return nil
}

// drain returns the accumulated panels and resets the buffer in one
// atomic pop. Callers shouldn't call drain concurrently with Render
// from multiple goroutines (the handler does it after executor.Run
// returns, single-threaded by then).
func (b *mcpRenderBridge) drain() []pluginRuntime.Panel {
	b.mu.Lock()
	out := b.panels
	b.panels = nil
	b.mu.Unlock()
	return out
}

// renderPanelsForUnstructuredContent renders each panel to ASCII
// (via the TUI's renderPanelASCII so the wire shape on TUI / ACP /
// MCP stays visually identical) and joins them onto the existing
// content string with a leading divider. The MCP spec asks structured
// content to be accompanied by "functionally equivalent unstructured
// content"; the ASCII rendering IS that fallback for clients that
// don't yet decode the structured panel field.
func renderPanelsForUnstructuredContent(textBefore string, panels []pluginRuntime.Panel) string {
	if len(panels) == 0 {
		return textBefore
	}
	var b strings.Builder
	b.WriteString(textBefore)
	for _, p := range panels {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(tui.RenderPanelASCIIPublic(p))
	}
	return b.String()
}

// panelsToStructured converts the panel slice into the wire shape
// returned in CallToolResult.StructuredContent. Same shape the ACP
// session/update kind=panel notification uses (mirrors
// `internal/acp/render_bridge.go::panelToWire`) so a client that
// understands the ACP wire format already understands MCP's. F9b.4.
func panelsToStructured(panels []pluginRuntime.Panel) map[string]any {
	if len(panels) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(panels))
	for _, p := range panels {
		out = append(out, panelToWireForMCP(p))
	}
	return map[string]any{"panels": out}
}

// panelToWireForMCP duplicates the (small) panel-to-wire conversion
// rather than importing the ACP package's unexported helper — the
// two surfaces are independent by design (different protocols, no
// shared dependency yet) and the per-section body switch is six
// short cases. If a third surface needs the same shape we'll lift
// the helper into pluginRuntime alongside the Panel type.
func panelToWireForMCP(p pluginRuntime.Panel) map[string]any {
	out := map[string]any{
		"title":    p.Title,
		"sections": sectionsToWireForMCP(p.Sections),
	}
	if p.Variant != "" {
		out["variant"] = p.Variant
	}
	if p.ID != "" {
		out["id"] = p.ID
	}
	if p.Footer != "" {
		out["footer"] = p.Footer
	}
	return out
}

func sectionsToWireForMCP(secs []pluginRuntime.Section) []map[string]any {
	out := make([]map[string]any, 0, len(secs))
	for _, sec := range secs {
		w := map[string]any{"kind": sec.Kind}
		if sec.Heading != "" {
			w["heading"] = sec.Heading
		}
		switch sec.Kind {
		case "text":
			w["text"] = sec.Text
		case "kv":
			pairs := make([]map[string]any, 0, len(sec.KV))
			for _, p := range sec.KV {
				pairs = append(pairs, map[string]any{"label": p.Label, "value": p.Value})
			}
			w["kv"] = pairs
		case "list":
			body := map[string]any{"items": sec.List.Items}
			if sec.List.Marker != "" {
				body["marker"] = sec.List.Marker
			}
			w["list"] = body
		case "code":
			body := map[string]any{"content": sec.Code.Content}
			if sec.Code.Language != "" {
				body["language"] = sec.Code.Language
			}
			w["code"] = body
		case "table":
			w["table"] = map[string]any{
				"columns": sec.Table.Columns,
				"rows":    sec.Table.Rows,
			}
		case "diff":
			w["diff"] = map[string]any{
				"before": sec.Diff.Before,
				"after":  sec.Diff.After,
			}
		}
		out = append(out, w)
	}
	return out
}
