package headless

import (
	"context"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// headlessRenderBridge implements pluginRuntime.RenderBridge for the
// headless JSON-RPC server. Fire-and-forget: each panel emit becomes
// a `session.update kind=panel` notification on the existing wire,
// reusing the same channel context_warning / plugin_fork / tool_call
// already use. Same shape as the ACP server's kind=panel
// notification (mirrors `internal/acp/render_bridge.go`) so a client
// that decodes one understands the other.
//
// Why a separate file (vs. fold into plugins.go): the render path
// is independent of the session-bridge plumbing, has its own bridge
// adapter type, and may grow per-channel formatting later. Symmetric
// with `internal/acp/render_bridge.go`. F9b.5.
type headlessRenderBridge struct {
	server    *Server
	sessionID string
}

// Render satisfies pluginRuntime.RenderBridge. Drop-on-nil-conn is
// the standard contract — the server may have been torn down between
// dispatch and the plugin's first emit; refusing to error keeps the
// fire-and-forget surface honest.
func (b *headlessRenderBridge) Render(_ context.Context, panel pluginRuntime.Panel) error {
	if b == nil || b.server == nil || b.server.conn == nil {
		return nil
	}
	_ = b.server.conn.Notify("session.update", map[string]any{
		"sessionId": b.sessionID,
		"kind":      "panel",
		"panel":     headlessPanelToWire(panel),
	})
	return nil
}

// headlessPanelToWire converts a runtime Panel into the wire shape
// the JSON-RPC client receives. Mirrors
// `internal/acp/render_bridge.go::panelToWire` and
// `cmd/stado/mcp_render_bridge.go::panelToWireForMCP`. If a fourth
// surface needs the same conversion the helper graduates to a
// shared package; for now three lightly-coupled copies keep each
// surface independent.
func headlessPanelToWire(p pluginRuntime.Panel) map[string]any {
	out := map[string]any{
		"title":    p.Title,
		"sections": headlessSectionsToWire(p.Sections),
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

func headlessSectionsToWire(secs []pluginRuntime.Section) []map[string]any {
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

// Compile-time interface assertion so a future refactor that drops
// the Render method can't accidentally silently disable the wiring.
var _ pluginRuntime.RenderBridge = (*headlessRenderBridge)(nil)
