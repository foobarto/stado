package acp

import (
	"context"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// emitRenderPanel sends a stado_ui_render fire-and-forget panel to the
// ACP client over `session/update kind=panel`. Spec: F9b.3
// (.agent/specs/open/f9b-ui-render.md). Companion to emitToolSummary
// and emitSubagentUpdate — same drop-on-nil-conn discipline so a
// disconnected client doesn't error the plugin.
//
// Wire shape:
//
//	{
//	  "sessionId": "<session id>",
//	  "kind":      "panel",
//	  "panel":     <renderRequestWire shape — see panelToWire below>
//	}
//
// Clients that don't yet render structured panels can fall back to
// the panel's title + first text section's body to construct a
// minimal text view (graceful degradation, same posture F10's
// `kind=choice` per-option `input` field takes for older clients).
func (s *Server) emitRenderPanel(sessionID string, panel pluginRuntime.Panel) {
	if s == nil || s.conn == nil {
		return
	}
	_ = s.conn.Notify("session/update", map[string]any{
		"sessionId": sessionID,
		"kind":      "panel",
		"panel":     panelToWire(panel),
	})
}

// panelToWire converts a runtime Panel into the JSON wire shape the
// ACP client receives. Mirrors the `renderRequestWire` decoder in
// `internal/plugins/runtime/host_ui_render.go` so the round-trip is
// symmetric: plugin author marshals → host decoder → server
// notification → client decoder gets the same logical structure.
//
// Sectional bodies are emitted only when the relevant kind matches —
// no spurious empty-body fields slip through, and clients can switch
// on `section.kind` without defensive nil checks for the wrong
// shape.
func panelToWire(p pluginRuntime.Panel) map[string]any {
	out := map[string]any{
		"title":    p.Title,
		"sections": sectionsToWire(p.Sections),
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

func sectionsToWire(secs []pluginRuntime.Section) []map[string]any {
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

// Render implements pluginRuntime.RenderBridge over the ACP host —
// picked up by pluginrun's attachLifecycleBridges via interface
// assertion (same path that wires Approval and Choice). Fire-and-
// forget: returns nil immediately after the notification is queued.
// Spec: F9b.3.
func (h *acpHost) Render(_ context.Context, panel pluginRuntime.Panel) error {
	if h.server == nil {
		return nil // no server attached — drop on the floor
	}
	h.server.emitRenderPanel(h.sessionID, panel)
	return nil
}
