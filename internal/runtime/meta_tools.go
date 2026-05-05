package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// registerMetaTools adds the four dispatch-kernel tools to reg.
// These are native Go implementations (EP-0037); they port to wasm in EP-0038.
func registerMetaTools(reg *tools.Registry) {
	reg.Register(&metaSearch{reg: reg})
	reg.Register(&metaDescribe{reg: reg})
	reg.Register(&metaCategories{reg: reg})
	reg.Register(&metaInCategory{reg: reg})
}

// ── tools__search ──────────────────────────────────────────────────────────

type metaSearch struct{ reg *tools.Registry }

func (m *metaSearch) Name() string { return "tools__search" }
func (m *metaSearch) Description() string {
	return "Search available tools by name, summary, or category. No-arg form lists all enabled tools (light shape: name, summary, categories)."
}
func (m *metaSearch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Optional substring filter on name, description, or categories."},
			"limit": map[string]any{"type": "integer", "description": "Max results (default 200)."},
		},
	}
}
func (m *metaSearch) Run(_ context.Context, args json.RawMessage, _ pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{}, fmt.Errorf("metaSearch: parse args: %w", err)
	}
	if req.Limit <= 0 {
		req.Limit = 200
	}
	q := strings.ToLower(req.Query)
	var out []map[string]any
	for _, t := range m.reg.All() {
		if isMetaTool(t.Name()) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(t.Name() + " " + t.Description())
			if !strings.Contains(hay, q) {
				continue
			}
		}
		entry := map[string]any{
			"name":    t.Name(),
			"summary": summarise(t.Description()),
		}
		if tc, ok := t.(toolCategoried); ok {
			entry["categories"] = tc.Categories()
		}
		out = append(out, entry)
		if len(out) >= req.Limit {
			break
		}
	}
	resp := map[string]any{"tools": out, "truncated": len(out) == req.Limit}
	b, _ := json.Marshal(resp)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__describe ────────────────────────────────────────────────────────

type metaDescribe struct{ reg *tools.Registry }

func (m *metaDescribe) Name() string { return "tools__describe" }
func (m *metaDescribe) Description() string {
	return "Fetch full schema + docs for named tools and activate them for this session. Batched: pass multiple names in one call."
}
func (m *metaDescribe) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"names"},
		"properties": map[string]any{
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Canonical or wire-form tool names.",
			},
		},
	}
}
func (m *metaDescribe) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	var out []map[string]any
	for _, name := range req.Names {
		t, ok := m.reg.Get(name)
		if !ok {
			out = append(out, map[string]any{"name": name, "error": "not found"})
			continue
		}
		schema, _ := json.Marshal(t.Schema())
		entry := map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"schema":      json.RawMessage(schema),
		}
		if tc, ok := t.(toolCategoried); ok {
			entry["categories"] = tc.Categories()
		}
		out = append(out, entry)
		if ta, ok := h.(pkgtool.ToolActivator); ok {
			ta.ActivateTool(name)
		}
	}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__categories ─────────────────────────────────────────────────────

type metaCategories struct{ reg *tools.Registry }

func (m *metaCategories) Name() string { return "tools__categories" }
func (m *metaCategories) Description() string {
	return "List canonical categories that currently-enabled tools belong to. Optional substring filter."
}
func (m *metaCategories) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Optional substring filter on category name."},
		},
	}
}
func (m *metaCategories) Run(_ context.Context, args json.RawMessage, _ pkgtool.Host) (pkgtool.Result, error) {
	var req struct{ Query string `json:"query"` }
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{}, fmt.Errorf("metaCategories: parse args: %w", err)
	}
	q := strings.ToLower(req.Query)
	seen := map[string]bool{}
	for _, t := range m.reg.All() {
		if tc, ok := t.(toolCategoried); ok {
			for _, c := range tc.Categories() {
				if q == "" || strings.Contains(strings.ToLower(c), q) {
					seen[c] = true
				}
			}
		}
	}
	cats := make([]string, 0, len(seen))
	for c := range seen {
		cats = append(cats, c)
	}
	b, _ := json.Marshal(cats)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__in_category ────────────────────────────────────────────────────

type metaInCategory struct{ reg *tools.Registry }

func (m *metaInCategory) Name() string { return "tools__in_category" }
func (m *metaInCategory) Description() string {
	return "List tools in a specific category (exact canonical name or extra_category value)."
}
func (m *metaInCategory) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Exact category name."},
		},
	}
}
func (m *metaInCategory) Run(_ context.Context, args json.RawMessage, _ pkgtool.Host) (pkgtool.Result, error) {
	var req struct{ Name string `json:"name"` }
	if err := json.Unmarshal(args, &req); err != nil || req.Name == "" {
		return pkgtool.Result{Error: "name is required"}, nil
	}
	var out []map[string]any
	for _, t := range m.reg.All() {
		if tc, ok := t.(toolCategoried); ok {
			for _, c := range tc.Categories() {
				if c == req.Name {
					out = append(out, map[string]any{
						"name":       t.Name(),
						"summary":    summarise(t.Description()),
						"categories": tc.Categories(),
					})
					break
				}
			}
		}
	}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

type toolCategoried interface {
	Categories() []string
}

func summarise(desc string) string {
	if len(desc) <= 100 {
		return desc
	}
	return desc[:97] + "..."
}
