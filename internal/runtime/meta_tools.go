package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// registerMetaTools adds the dispatch-kernel tools to reg.
// These are native Go implementations (EP-0037); they port to wasm in EP-0038.
func registerMetaTools(reg *tools.Registry) {
	reg.Register(&metaSearch{reg: reg})
	reg.Register(&metaDescribe{reg: reg})
	reg.Register(&metaCategories{reg: reg})
	reg.Register(&metaInCategory{reg: reg})
	reg.Register(&metaActivate{reg: reg})
	reg.Register(&metaDeactivate{reg: reg})
	reg.Register(&metaPluginLoad{reg: reg})
	reg.Register(&metaPluginUnload{reg: reg})
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
	return "Fetch full schema + docs for named tools and activate them for this session. Pass either name=\"foo\" for a single tool or names=[\"foo\",\"bar\"] for a batch — both forms accepted in one round-trip."
}
func (m *metaDescribe) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Single tool name (canonical or wire-form). Use this OR `names` for a batch.",
			},
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Multiple tool names (canonical or wire-form). Use this OR `name`. When both are passed, entries are merged (duplicates deduped).",
			},
		},
	}
}
func (m *metaDescribe) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	// Merge `name` + `names`, dedupe while preserving caller order.
	queryNames := make([]string, 0, len(req.Names)+1)
	seen := map[string]bool{}
	for _, n := range append([]string{req.Name}, req.Names...) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		queryNames = append(queryNames, n)
	}
	if len(queryNames) == 0 {
		return pkgtool.Result{Error: "tools__describe: provide `name` or `names`"}, nil
	}
	var out []map[string]any
	for _, name := range queryNames {
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

// ── tools__activate ────────────────────────────────────────────────────────

type metaActivate struct{ reg *tools.Registry }

func (m *metaActivate) Name() string { return "tools__activate" }
func (m *metaActivate) Description() string {
	return "Add a tool (or list of tools) to this session's per-turn tool surface. Use when a parent agent has already told you the tool's name and you don't need a full schema fetch — call activate to skip the tools.describe round-trip. Returns the list of names actually surfaced (unknown names report as `error: not found`)."
}
func (m *metaActivate) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Single tool name (canonical or wire-form).",
			},
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Multiple tool names. Use this OR `name`.",
			},
		},
	}
}
func (m *metaActivate) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	queryNames := mergeNames(req.Name, req.Names)
	if len(queryNames) == 0 {
		return pkgtool.Result{Error: "tools__activate: provide `name` or `names`"}, nil
	}
	activator, _ := h.(pkgtool.ToolActivator)
	var out []map[string]any
	for _, name := range queryNames {
		t, ok := m.reg.Get(name)
		if !ok {
			out = append(out, map[string]any{"name": name, "error": "not found"})
			continue
		}
		if activator != nil {
			activator.ActivateTool(t.Name())
		}
		out = append(out, map[string]any{"name": t.Name(), "activated": true})
	}
	if activator == nil {
		// Activation is a no-op without a host that supports it; the
		// caller should know — surface as an error result rather than
		// silently succeeding.
		return pkgtool.Result{Error: "tools__activate: current host does not support activation (running outside a session?)"}, nil
	}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__deactivate ──────────────────────────────────────────────────────

type metaDeactivate struct{ reg *tools.Registry }

func (m *metaDeactivate) Name() string { return "tools__deactivate" }
func (m *metaDeactivate) Description() string {
	return "Remove a tool (or list of tools) from this session's per-turn surface. Inverse of tools.activate. Useful when switching tasks — drop tools from prior phases to keep context lean. Tools in [tools].autoload are not affected (autoload is config-driven, not session-scoped)."
}
func (m *metaDeactivate) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  map[string]any{"type": "string"},
			"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}
func (m *metaDeactivate) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	queryNames := mergeNames(req.Name, req.Names)
	if len(queryNames) == 0 {
		return pkgtool.Result{Error: "tools__deactivate: provide `name` or `names`"}, nil
	}
	deactivator, _ := h.(pkgtool.ToolDeactivator)
	if deactivator == nil {
		return pkgtool.Result{Error: "tools__deactivate: current host does not support deactivation"}, nil
	}
	var out []map[string]any
	for _, name := range queryNames {
		// Don't require the tool exist — deactivate is idempotent.
		deactivator.DeactivateTool(name)
		out = append(out, map[string]any{"name": name, "deactivated": true})
	}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── plugin__load ───────────────────────────────────────────────────────────

type metaPluginLoad struct{ reg *tools.Registry }

func (m *metaPluginLoad) Name() string { return "plugin__load" }
func (m *metaPluginLoad) Description() string {
	return "Activate every tool exposed by a plugin into this session's surface in one call. Sugar over tools.activate for whole-plugin loading. Args: plugin name (bare, no version)."
}
func (m *metaPluginLoad) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"plugin"},
		"properties": map[string]any{
			"plugin": map[string]any{
				"type":        "string",
				"description": "Plugin name (bare form — no version suffix). Matches `stado plugin info <name>`.",
			},
		},
	}
}
func (m *metaPluginLoad) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct{ Plugin string `json:"plugin"` }
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	if req.Plugin == "" {
		return pkgtool.Result{Error: "plugin__load: `plugin` required"}, nil
	}
	toolNames := pluginToolNames(m.reg, req.Plugin)
	if len(toolNames) == 0 {
		return pkgtool.Result{Error: fmt.Sprintf("plugin__load: no tools found for plugin %q", req.Plugin)}, nil
	}
	activator, _ := h.(pkgtool.ToolActivator)
	if activator == nil {
		return pkgtool.Result{Error: "plugin__load: current host does not support activation"}, nil
	}
	for _, n := range toolNames {
		activator.ActivateTool(n)
	}
	out := map[string]any{"plugin": req.Plugin, "activated": toolNames}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── plugin__unload ─────────────────────────────────────────────────────────

type metaPluginUnload struct{ reg *tools.Registry }

func (m *metaPluginUnload) Name() string { return "plugin__unload" }
func (m *metaPluginUnload) Description() string {
	return "Inverse of plugin.load — deactivate every tool exposed by the named plugin. Args: plugin name (bare)."
}
func (m *metaPluginUnload) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"plugin"},
		"properties": map[string]any{
			"plugin": map[string]any{"type": "string"},
		},
	}
}
func (m *metaPluginUnload) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct{ Plugin string `json:"plugin"` }
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	if req.Plugin == "" {
		return pkgtool.Result{Error: "plugin__unload: `plugin` required"}, nil
	}
	toolNames := pluginToolNames(m.reg, req.Plugin)
	deactivator, _ := h.(pkgtool.ToolDeactivator)
	if deactivator == nil {
		return pkgtool.Result{Error: "plugin__unload: current host does not support deactivation"}, nil
	}
	for _, n := range toolNames {
		deactivator.DeactivateTool(n)
	}
	out := map[string]any{"plugin": req.Plugin, "deactivated": toolNames}
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

// mergeNames combines a single `name` with a slice `names`, dedupes, and
// preserves caller order (name first).
func mergeNames(name string, names []string) []string {
	out := make([]string, 0, len(names)+1)
	seen := map[string]bool{}
	for _, n := range append([]string{name}, names...) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// pluginToolNames returns every tool name the named plugin contributes
// to reg. Matches via tool_metadata.LookupToolMetadata's Plugin field
// when set, falling back to wire-form prefix match (`<plugin>__<tool>`)
// for tools without metadata. Returns nil when no match.
func pluginToolNames(reg *tools.Registry, plugin string) []string {
	var out []string
	prefix := plugin + "__"
	for _, t := range reg.All() {
		name := t.Name()
		if md := LookupToolMetadata(name); md.Plugin == plugin {
			out = append(out, name)
			continue
		}
		// Wire-form fallback: name starts with `<plugin>__` (e.g.
		// gtfobins_lookup → plugin "gtfobins"). Single-underscore
		// wire convention (installed plugins) AND double-underscore
		// (bundled) both checked.
		if strings.HasPrefix(name, prefix) || strings.HasPrefix(name, plugin+"_") {
			out = append(out, name)
		}
	}
	return out
}
