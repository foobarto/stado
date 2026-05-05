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

// defaultAutoloadNames is the hardcoded convenience core when [tools.autoload]
// is empty. These bare names match the pre-EP-0038 native tool names and will
// be updated to wire names (fs__read etc.) when EP-0038 migrates each tool.
var defaultAutoloadNames = []string{
	"read", "write", "edit", "glob", "grep", "bash",
	// EP-0038 wire names for new wasm-backed tools. fs__ls supersedes
	// the bare `ls` native tool, which is hidden from listings — no
	// bare `ls` registration exists in the registry post-EP-0038.
	"fs__ls",
	// spawn_agent: native subagent tool needs autoload — its SubagentEvent
	// path is wired only for this tool, not for the wasm agent__spawn alias.
	// TODO: collapse spawn_agent / agent__spawn into one canonical surface
	// (route the wasm wrapper to the native SubagentEvent path) so the
	// LLM doesn't see two semantically-equivalent spawn tools.
	"spawn_agent",
}

// BuildDefaultRegistry returns a Registry preloaded with stado's bundled tools
// (bash, fs, webfetch). Separate from Executor so callers can add/remove tools
// before constructing the Executor.
func BuildDefaultRegistry() *tools.Registry {
	reg := buildBundledPluginRegistry()
	registerMetaTools(reg)
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
		wirePrefix := strings.NewReplacer(".", "_", "-", "_").Replace(rest) + "__"
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
	var out []pkgtool.Tool
	for _, t := range reg.All() {
		if isMetaTool(t.Name()) {
			out = append(out, t)
			continue
		}
		if toolMatchesAny(t.Name(), autoloadPatterns) {
			out = append(out, t)
		}
	}
	return out
}

// isMetaTool reports whether name is one of the four dispatch kernel tools.
func isMetaTool(name string) bool {
	switch name {
	case "tools__search", "tools__describe", "tools__categories", "tools__in_category":
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
	reg := BuildDefaultRegistry()
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
