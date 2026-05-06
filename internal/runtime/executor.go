package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/tasktool"
	pkgtool "github.com/foobarto/stado/pkg/tool"
	"github.com/foobarto/stado/pkg/agent"
)

// defaultAutoloadNames is the hardcoded convenience core when
// [tools.autoload] is empty. Each entry must match a registered tool's
// Name() exactly — the autoload selection is by-name, not by-canonical
// dotted form, so the mixed shapes below are deliberate and reflect
// what the registry actually contains.
//
// Legacy native fs/bash tools register under bare names (read, write,
// edit, glob, grep, bash) per their Tool.Name() implementations in
// internal/tools/fs and internal/tools/bash. They're wrapped at
// registration time by newBundledPluginTool, which preserves the bare
// name — there's no fs__read alias in the registry today, so switching
// these entries to wire form would silently break autoload.
//
// EP-0038-migrated tools (only fs__ls today) register under wire form;
// they appear here in wire form for the same reason.
//
// agent__spawn is the wasm-backed canonical surface for sub-agent spawning;
// it routes through SubagentRunner.SpawnSubagent and emits SubagentEvent
// for full lifecycle observability. The native spawn_agent registration has
// been removed (BACKLOG #1).
//
// To convert a bare-name entry to wire form, also add the wire-form
// alias at registration time in bundled_plugin_tools.go.
var defaultAutoloadNames = []string{
	"read", "write", "edit", "glob", "grep", "bash",
	"fs__ls",
	"agent__spawn",
}

// BuildDefaultRegistry returns a Registry preloaded with stado's
// bundled tools (fs, shell, web, dns, agent, etc.), the meta-tools
// (tools__search/describe/categories/in_category), and — when cfg
// is non-nil — the operator's installed plugins from cfg.StateDir()/
// plugins/. Bundled registers first; installed registers last and
// overwrites bundled on tool-name collision (Q4 — installed wins).
//
// cfg may be nil for test code that wants the bundled-only set;
// production callers should pass the loaded config.
func BuildDefaultRegistry(cfg *config.Config) *tools.Registry {
	reg := buildBundledPluginRegistry()
	registerMetaTools(reg)
	if cfg != nil {
		registerInstalledPluginTools(reg, cfg)
	}
	return reg
}

// ToolMatchesGlob reports whether a registered tool name matches a config
// pattern. Patterns are either exact names (bare or wire-form) or wildcard
// globs using dotted-canonical syntax with a trailing .*:
//
//   - "read"     — exact bare-name match
//   - "fs.*"     — matches any wire-form tool whose alias segment is "fs"
//     (fs__read, fs__write, etc.) or canonical form fs.read
//   - "*"        — matches every tool
//
// Wire form uses double-underscore as separator (EP-0037 §B), so "fs.*"
// maps to the prefix "fs__" when matching wire names.
func ToolMatchesGlob(toolName, pattern string) bool {
	// Universal wildcard.
	if pattern == "*" {
		return true
	}
	// Exact match (bare names, wire names, or canonical dotted names).
	if toolName == pattern {
		return true
	}
	// Dotted wildcard: "fs.*" matches wire-form tools with alias "fs__"
	// and canonical-form tools with prefix "fs.".
	if rest, ok := strings.CutSuffix(pattern, ".*"); ok {
		wirePrefix := tools.WireSegment(rest) + "__"
		dotPrefix := rest + "."
		return strings.HasPrefix(toolName, wirePrefix) || strings.HasPrefix(toolName, dotPrefix)
	}
	return false
}

// toolMatchesAny returns true when toolName matches any of the patterns.
func toolMatchesAny(toolName string, patterns []string) bool {
	for _, p := range patterns {
		if ToolMatchesGlob(toolName, p) {
			return true
		}
	}
	return false
}

// AutoloadedTools returns the subset of tools in reg that should have their
// schemas sent to the model on every turn (EP-0037 §E). The four meta-tools
// are always included regardless of config. If cfg.Tools.Autoload is empty,
// defaultAutoloadNames is used.
func AutoloadedTools(reg *tools.Registry, cfg *config.Config) []pkgtool.Tool {
	autoloadPatterns := defaultAutoloadNames
	if cfg != nil && len(cfg.Tools.Autoload) > 0 {
		autoloadPatterns = cfg.Tools.Autoload
	}
	categorySet := map[string]bool{}
	if cfg != nil {
		for _, c := range cfg.Tools.AutoloadCategories {
			categorySet[c] = true
		}
	}
	seen := map[string]bool{}
	var out []pkgtool.Tool
	for _, t := range reg.All() {
		if isMetaTool(t.Name()) {
			if !seen[t.Name()] {
				out = append(out, t)
				seen[t.Name()] = true
			}
			continue
		}
		if toolMatchesAny(t.Name(), autoloadPatterns) {
			if !seen[t.Name()] {
				out = append(out, t)
				seen[t.Name()] = true
			}
			continue
		}
		// Category-based autoload (Tester #7). Tools whose Categories
		// metadata overlaps with cfg.Tools.AutoloadCategories join the
		// per-turn surface. Empty AutoloadCategories = no category-based
		// expansion.
		if len(categorySet) > 0 {
			if tc, ok := t.(toolCategoried); ok {
				for _, c := range tc.Categories() {
					if categorySet[c] {
						if !seen[t.Name()] {
							out = append(out, t)
							seen[t.Name()] = true
						}
						break
					}
				}
			}
		}
	}
	return out
}

// isMetaTool reports whether name is one of the dispatch kernel tools.
// All meta-tools are unconditionally autoloaded — they're how the model
// discovers and activates the rest of the surface.
func isMetaTool(name string) bool {
	switch name {
	case "tools__search", "tools__describe", "tools__categories", "tools__in_category",
		"tools__activate", "tools__deactivate", "plugin__load", "plugin__unload":
		return true
	}
	return false
}

// ApplyToolFilter trims a registry per cfg.Tools. All tools are on by default;
// Enabled acts as an allowlist (keep only these); Disabled removes specific
// names. When both are set Enabled wins. Patterns support wildcard globs via
// ToolMatchesGlob (e.g. "fs.*", "*"). Zero-match globs are silent no-ops.
//
// Mutates the registry in place; safe to chain after BuildDefaultRegistry.
func ApplyToolFilter(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Tools.Enabled) == 0 && len(cfg.Tools.Disabled) == 0 {
		return
	}
	known := map[string]bool{}
	for _, t := range reg.All() {
		known[t.Name()] = true
	}

	// Warn only for exact (non-glob) names that don't match anything.
	warnUnknownExact := func(list []string, label string) {
		for _, n := range list {
			if strings.ContainsAny(n, "*?") {
				continue // globs: zero match is silent
			}
			if !known[n] {
				fmt.Fprintf(os.Stderr, "stado: [tools].%s mentions %q — no such tool (ignored)\n", label, n)
			}
		}
	}
	warnUnknownExact(cfg.Tools.Enabled, "enabled")
	warnUnknownExact(cfg.Tools.Disabled, "disabled")

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		for name := range known {
			if toolMatchesAny(name, cfg.Tools.Enabled) {
				allow[name] = true
			}
		}
		if len(allow) == 0 {
			return
		}
		for name := range known {
			if !allow[name] {
				reg.Unregister(name)
			}
		}
		return
	}
	// Disabled-only path.
	for name := range known {
		if toolMatchesAny(name, cfg.Tools.Disabled) {
			reg.Unregister(name)
		}
	}
}

// BuildExecutor wires the tool registry + session + sandbox runner.
//
// Also loads any MCP servers from config and registers their tools. Failed
// MCP connections are logged to stderr, not fatal — stado should boot
// without them if the endpoint is down.
//
// Respects cfg.Tools.Enabled / Disabled — the user's allowlist /
// blocklist is applied AFTER MCP tools land so MCP-sourced names can
// also be trimmed.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string) (*tools.Executor, error) {
	reg := BuildDefaultRegistry(cfg)
	reg.Register(tasktool.Tool{Path: tasks.StorePath(cfg.StateDir())})

	if len(cfg.MCP.Servers) > 0 {
		if err := attachMCP(reg, cfg.MCP.Servers); err != nil {
			fmt.Fprintf(os.Stderr, "stado: MCP setup: %v\n", err)
		}
	}
	ApplyWasmMigration(reg, cfg)
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		return nil, err
	}
	ApplyToolFilter(reg, cfg)

	return &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Agent:    agentName,
		Model:    cfg.Defaults.Model,
		ReadLog:  tools.NewReadLog(),
	}, nil
}

// attachMCP is defined in mcp_glue.go — kept in a separate file so pulling
// the MCP SDK in is a single-file diff and easier to #ifdef out on airgap
// builds later.

// ToolDefsFromSlice renders a tool slice as []agent.ToolDef. Used by the
// agentloop to send only the autoloaded + activated subset each turn.
func ToolDefsFromSlice(ts []pkgtool.Tool) []agent.ToolDef {
	out := make([]agent.ToolDef, 0, len(ts))
	for _, t := range ts {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

// ToolDefs renders the registry as []agent.ToolDef for a TurnRequest.
func ToolDefs(reg *tools.Registry) []agent.ToolDef {
	if reg == nil {
		return nil
	}
	all := reg.All()
	out := make([]agent.ToolDef, 0, len(all))
	for _, t := range all {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

func allowedToolSet(defs []agent.ToolDef) map[string]struct{} {
	out := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		out[def.Name] = struct{}{}
	}
	return out
}

func toolAllowed(allowed map[string]struct{}, name string) bool {
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[name]
	return ok
}

func unavailableToolResult(name string) string {
	return fmt.Sprintf("tool %q is not available for this turn", name)
}

// activatedSlice returns the tools in reg whose names are in the activated set.
func activatedSlice(reg *tools.Registry, activated map[string]bool) []pkgtool.Tool {
	out := make([]pkgtool.Tool, 0, len(activated))
	for name := range activated {
		if t, ok := reg.Get(name); ok {
			out = append(out, t)
		}
	}
	return out
}

// dedupeTools returns ts with duplicate names removed (first occurrence wins).
func dedupeTools(ts []pkgtool.Tool) []pkgtool.Tool {
	seen := make(map[string]bool, len(ts))
	out := make([]pkgtool.Tool, 0, len(ts))
	for _, t := range ts {
		if !seen[t.Name()] {
			seen[t.Name()] = true
			out = append(out, t)
		}
	}
	return out
}

// extractActivated parses a tools.describe result JSON and adds the names of
// successfully described tools to the activated set.
func extractActivated(content string, activated map[string]bool) {
	AbsorbActivatedFromDescribe(content, activated)
}

// AbsorbActivatedFromDescribe is the exported form of extractActivated.
// Used by the TUI's per-session activation tracking (model_stream.go's
// absorbToolActivations) so the lazy-load surface flips on after the
// model calls tools.describe — matching the headless agentloop's
// behaviour at internal/runtime/agentloop.go's activatedNames tracking.
func AbsorbActivatedFromDescribe(content string, activated map[string]bool) {
	var items []map[string]any
	if err := json.Unmarshal([]byte(content), &items); err != nil {
		return
	}
	for _, item := range items {
		name, ok := item["name"].(string)
		if !ok {
			continue
		}
		if _, hasErr := item["error"]; !hasErr {
			activated[name] = true
		}
	}
}
