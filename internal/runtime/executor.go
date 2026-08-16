package runtime

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// defaultAutoloadNames is the hardcoded convenience core when
// [tools.autoload] is empty. Each entry must match a registered tool's
// Name() exactly — the autoload selection is by-name, not by-canonical
// dotted form, so the mixed shapes below are deliberate and reflect
// what the registry actually contains.
//
// Bundled tool names are the exact model-facing names authenticated by each
// source-adjacent embedded manifest.
//
// agent__spawn is the wasm-backed canonical surface for sub-agent spawning;
// it routes through SubagentRunner.SpawnSubagent and emits SubagentEvent
// for full lifecycle observability. The native spawn_agent registration has
// been removed (BACKLOG #1).
//
// To convert a bare-name entry to wire form, also add the wire-form
// alias at registration time in bundled_plugin_tools.go.
var defaultAutoloadNames = []string{
	"fs__read", "fs__write", "fs__edit", "fs__glob", "fs__grep", "shell__bash",
	"fs__ls",
	"agent__spawn",
}

// DefaultAutoloadNames returns a copy of the runtime's built-in
// autoload set. Exposed for callers (notably the TUI session-override
// path) that need to materialize the defaults into an effective Autoload
// list when the operator has cleared / removed entries via `/tool
// unautoload`, so the override actually takes effect instead of
// reverting to the implicit defaults (Codex #088).
func DefaultAutoloadNames() []string {
	out := make([]string, len(defaultAutoloadNames))
	copy(out, defaultAutoloadNames)
	return out
}

// CanonicalToolName returns the canonical (dotted) form of a tool name
// when the input is a wire-form (`fs__read` → `fs.read`). Pure
// passthrough for inputs that are already canonical, single-segment, or
// otherwise unparseable. Useful for canonical-equivalence dedup in
// callers that need to treat `shell__bash` and `shell.bash` as the
// same entry (e.g. the TUI session-override materializer per Copilot
// review on #50 round 1).
func CanonicalToolName(name string) string {
	if alias, sub, ok := tools.ParseWireForm(name); ok {
		return alias + "." + sub
	}
	return name
}

// BuildDefaultRegistry returns a Registry preloaded with stado's bundled WASM
// tools and—when cfg is non-nil—the operator's admitted installed plugins.
// Installed packages are
// collected and collision-preflighted before any of their tools register; a
// map iteration or display alias never selects a winner.
//
// cfg may be nil for test code that wants the bundled-only set;
// production callers should pass the loaded config.
func BuildDefaultRegistry(cfg *config.Config) *tools.Registry {
	return buildDefaultRegistryForSurface(cfg, nil, "")
}

func buildDefaultRegistryForSurface(cfg *config.Config, exactAgentChildTools map[string]bool, childToolOwner string) *tools.Registry {
	reg := buildBundledPluginRegistry()
	if cfg != nil {
		registerInstalledPluginToolsForSurface(reg, cfg, exactAgentChildTools, childToolOwner)
	}
	// Codex C4/Q P2: bind cfg + the final composed registry onto every
	// bundled tool that was registered above. Pre-fix the bundled
	// pluginTool.Run path read process-wide globals
	// (installedRunCfg / installedInvokeReg) that got REBOUND by every
	// subsequent BuildDefaultRegistry call — e.g. a /tool info build's
	// unfiltered registry leaked into the in-flight session's bundled
	// dispatch surface, silently widening it past [tools].enabled. Now
	// each build's bundled tools are anchored to their own build's
	// registry pointer + cfg, so independent builds don't interfere.
	for _, t := range reg.All() {
		if b, ok := t.(*bundledPluginTool); ok {
			b.setRuntime(cfg, reg)
		}
	}
	return reg
}

// BuildDefaultRegistryQuiet is for registry rebuilds after a TUI has entered
// its alternate screen. Build failures still return through their normal API;
// advisory diagnostics are suppressed so they cannot corrupt the display.
func BuildDefaultRegistryQuiet(cfg *config.Config) *tools.Registry {
	var reg *tools.Registry
	withRegistryDiagnosticsSuppressed(func() {
		reg = BuildDefaultRegistry(cfg)
	})
	return reg
}

// PinInvokeExecutor wires the active executor onto every plugin tool that
// can dispatch nested stado_tool_invoke calls. Must run after the
// executor is constructed so nested invokes route through Executor.Run
// (audit trailers, hooks, progress) instead of Registry.Run (Codex #043).
func PinInvokeExecutor(reg *tools.Registry, exec *tools.Executor) {
	if reg == nil || exec == nil {
		return
	}
	for _, t := range reg.All() {
		switch v := t.(type) {
		case *bundledPluginTool:
			v.invokeExec = exec
		case *installedPluginTool:
			v.invokeExec = exec
		case *pluginOverrideTool:
			v.invokeExec = exec
		}
	}
}

// ToolMatchesGlob reports whether a registered tool name matches a config
// pattern. Patterns are either exact names (bare, wire-form, or canonical
// dotted) or path.Match-style wildcard globs:
//
//   - "read"     — exact bare-name match
//   - "fs.read"  — exact canonical (dotted) match against a wire-form name
//   - "fs__read" — exact wire-form match
//   - "fs.*"     — matches any wire-form tool whose alias segment is "fs"
//     (fs__read, fs__write, etc.) or canonical form fs.read
//   - "shell_*"  — matches ordinary underscore-prefixed names such as
//     shell_create as well as wire-form names such as shell__bash
//   - "*"        — matches every tool
//
// Bundled tools register under wire-form names (`fs__read`, `shell__bash`)
// but operators reach for canonical names in config (`fs.read`,
// `shell.bash`). The canonical-vs-wire match below is what makes
// `[tools].enabled = ["fs.read"]` line up against the registered
// `fs__read`. Without it, exact-canonical-name patterns silently failed
// to match any tool, and the empty-allow fall-open in ApplyToolFilter
// turned that miss into "every tool stays enabled" — opposite of the
// operator's intent.
func ToolMatchesGlob(toolName, pattern string) bool {
	// Universal wildcard.
	if pattern == "*" {
		return true
	}
	// Exact match (bare names, wire names, or — when toolName is a wire
	// form whose canonical reconstruction equals pattern — canonical
	// dotted names).
	if toolName == pattern {
		return true
	}
	if alias, sub, ok := tools.ParseWireForm(toolName); ok {
		if alias+"."+sub == pattern {
			return true
		}
	}
	// General glob match against the exact registered name. The CLI and
	// config call these values globs, so ordinary patterns such as shell_*
	// must not fail closed merely because they aren't dotted namespace
	// wildcards. path.Match is deterministic across host platforms and tool
	// names cannot contain path separators.
	if matched, err := path.Match(pattern, toolName); err == nil && matched {
		return true
	}
	// Operators commonly use the canonical dotted form for a wire-form
	// registration. Match the same glob against that representation too, so
	// fs.* continues to match fs__read without inventing a second glob parser.
	if alias, sub, ok := tools.ParseWireForm(toolName); ok {
		if matched, err := path.Match(pattern, alias+"."+sub); err == nil && matched {
			return true
		}
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

// ToolPermittedByConfig reports whether one non-kernel tool survives the
// operator's global enabled/disabled ceiling. It is intentionally narrower
// than ApplyToolFilter: callers use it when a verified lifecycle application
// is admitted after the base registry has already been filtered, so late
// registration cannot bypass that earlier decision. Disabled always wins.
func ToolPermittedByConfig(toolName string, cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	if len(cfg.Tools.Enabled) > 0 && !toolMatchesAny(toolName, cfg.Tools.Enabled) {
		return false
	}
	return !toolMatchesAny(toolName, cfg.Tools.Disabled)
}

// AutoloadedTools returns the subset of tools in reg whose schemas are sent to
// the model on every turn (EP-0037 §E). There is no non-disableable kernel. If
// cfg.Tools.Autoload is empty, defaultAutoloadNames is used.
func AutoloadedTools(reg *tools.Registry, cfg *config.Config) []pkgtool.Tool {
	return AutoloadedToolsWithExtra(reg, cfg, nil)
}

// AutoloadedToolsWithExtra is AutoloadedTools plus an additional set of
// name/glob patterns merged into the per-turn autoload surface — ADDITIVELY
// (per the per-persona-skills-plugins decision, 2026-06-13). The active
// persona's EffectiveTools() are passed as extra so its declared
// `tools:`/`recommended_tools:` are promoted to the model-facing surface
// when it's active, without mutating shared cfg and without a registry
// rebuild (the tools are already IN the registry; autoload just selects
// them). A nil/empty extra is identical to plain AutoloadedTools.
//
// Extra is purely additive: it can promote a tool but never hides one that
// the cfg autoload set or the category expansion would have included.
func AutoloadedToolsWithExtra(reg *tools.Registry, cfg *config.Config, extra []string) []pkgtool.Tool {
	autoloadPatterns := defaultAutoloadNames
	if cfg != nil && len(cfg.Tools.Autoload) > 0 {
		autoloadPatterns = cfg.Tools.Autoload
	}
	if len(extra) > 0 {
		// Don't mutate the cfg-owned (or package-level default) slice:
		// build a fresh combined pattern list.
		combined := make([]string, 0, len(autoloadPatterns)+len(extra))
		combined = append(combined, autoloadPatterns...)
		combined = append(combined, extra...)
		autoloadPatterns = combined
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
		if toolMatchesAny(t.Name(), autoloadPatterns) {
			if !seen[t.Name()] {
				out = append(out, t)
				seen[t.Name()] = true
			}
			continue
		}
		// Category-based autoload (Tester #7). Tools whose category
		// manifest metadata overlaps with cfg.Tools.AutoloadCategories join
		// the per-turn surface. Empty AutoloadCategories means no
		// category-based expansion.
		if len(categorySet) > 0 {
			for _, c := range ToolMetadataFor(t).Categories {
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
	return out
}

// ApplyToolFilter trims a registry per cfg.Tools. All tools are on by default;
// Enabled acts as an allowlist (keep only these); Disabled removes specific
// names. When both are set DISABLED wins on overlap — Disabled is applied
// as a subtractive pass after the Enabled allowlist (Codex #096; pre-fix
// Enabled won, which made `enabled=["*"]` + `disable=["bash"]` leave bash
// registered). Patterns support wildcard globs via ToolMatchesGlob (e.g.
// "fs.*", "*"). Zero-match globs are silent no-ops; a non-empty Enabled
// that matches no tool fails CLOSED (registry emptied + stderr advisory).
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

	// Warn only for exact (non-glob) names that don't match anything,
	// in either wire form or canonical-vs-wire (so an operator typing
	// "fs.read" against the registered "fs__read" doesn't see a
	// misleading "no such tool" warning).
	warnUnknownExact := func(list []string, label string) {
		for _, n := range list {
			if strings.ContainsAny(n, "*?[") {
				continue // globs: zero match is silent
			}
			if known[n] {
				continue
			}
			matched := false
			for k := range known {
				if alias, sub, ok := tools.ParseWireForm(k); ok && alias+"."+sub == n {
					matched = true
					break
				}
			}
			if matched {
				continue
			}
			emitRegistryDiagnostic("stado: [tools].%s mentions %q — no such tool (ignored)\n", label, n)
		}
	}
	warnUnknownExact(cfg.Tools.Enabled, "enabled")
	warnUnknownExact(cfg.Tools.Disabled, "disabled")

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		matches := 0
		for name := range known {
			if toolMatchesAny(name, cfg.Tools.Enabled) {
				allow[name] = true
				matches++
			}
		}
		// Fail closed when the operator's allowlist matches no tool. The previous
		// fall-open ("return without filtering") defeated the
		// allowlist: typos / uninstalled references silently re-exposed the
		// whole surface. An empty match is more likely operator error than
		// intent — surface it loudly and remove the complete registry.
		if matches == 0 {
			emitRegistryDiagnostic(
				"stado: [tools].enabled = %v matched no registered tools — registry emptied (fail-closed). Did you mean canonical names like \"fs.read\" or globs like \"fs.*\"?\n",
				cfg.Tools.Enabled)
			for name := range known {
				reg.Unregister(name)
			}
			return
		}
		for name := range known {
			if !allow[name] {
				reg.Unregister(name)
			}
		}
		// Codex #096: when both lists are populated, apply Disabled
		// as a subtractive pass after the allowlist. Without this, an
		// allowlist of [`*`] + disable of `bash` left bash registered
		// (allow matched everything; Disabled was unreachable). Same
		// pattern for ["fs.*"] + disable ["fs.write"]: now fs.write
		// is correctly removed even though fs.* allowed it.
		for name := range known {
			if !allow[name] {
				continue
			}
			if toolMatchesAny(name, cfg.Tools.Disabled) {
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

// ApplyToolFilterQuiet applies the same fail-closed filtering without writing
// advisory diagnostics into a live TUI screen.
func ApplyToolFilterQuiet(reg *tools.Registry, cfg *config.Config) {
	withRegistryDiagnosticsSuppressed(func() {
		ApplyToolFilter(reg, cfg)
	})
}

// BuildRegistryWithPlugins builds the tool registry the agent loop,
// MCP server, and any other tool-dispatching surface should share.
// Composes:
//
//  1. BuildDefaultRegistry — bundled + installed plugin tools
//  2. external MCP-attached tools (when cfg.MCP.Servers non-empty)
//  3. ApplyToolOverrides (cfg.Tools.Overrides → pluginOverrideTool)
//  4. ApplyToolFilter (cfg.Tools.Enabled / Disabled allowlist)
//
// Every tool-dispatching surface uses this composition so MCP attachment,
// overrides, and config ceilings cannot drift between the agent/CLI/MCP paths.
func BuildRegistryWithPlugins(cfg *config.Config) (*tools.Registry, error) {
	return buildRegistryWithPluginsForSurface(cfg, nil, "")
}

func buildRegistryWithPluginsForSurface(cfg *config.Config, exactAgentChildTools map[string]bool, childToolOwner string) (*tools.Registry, error) {
	reg := buildDefaultRegistryForSurface(cfg, exactAgentChildTools, childToolOwner)

	if len(cfg.MCP.Servers) > 0 {
		if err := attachMCP(reg, cfg.MCP.Servers); err != nil {
			emitRegistryDiagnostic("stado: MCP setup: %v\n", err)
		}
	}
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		return nil, err
	}
	ApplyToolFilter(reg, cfg)
	return reg, nil
}

// BuildRegistryWithPluginsQuiet is the live-TUI counterpart of
// BuildRegistryWithPlugins.
func BuildRegistryWithPluginsQuiet(cfg *config.Config) (*tools.Registry, error) {
	var reg *tools.Registry
	var err error
	withRegistryDiagnosticsSuppressed(func() {
		reg, err = BuildRegistryWithPlugins(cfg)
	})
	return reg, err
}

// BuildExecutor wires the shared registry + session + sandbox runner.
//
// Respects cfg.Tools.Enabled / Disabled — the user's allowlist /
// blocklist is applied via BuildRegistryWithPlugins.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string, metrics telemetry.Metrics) (*tools.Executor, error) {
	return buildExecutorForSurface(sess, cfg, agentName, metrics, nil, "")
}

func buildExecutorForSurface(sess *stadogit.Session, cfg *config.Config, agentName string, metrics telemetry.Metrics, exactAgentChildTools map[string]bool, childToolOwner string) (*tools.Executor, error) {
	reg, err := buildRegistryWithPluginsForSurface(cfg, exactAgentChildTools, childToolOwner)
	if err != nil {
		return nil, err
	}
	exec := &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Metrics:  metrics,
		Agent:    agentName,
		Model:    cfg.Defaults.Model,
		ReadLog:  tools.NewReadLog(),
	}
	PinInvokeExecutor(reg, exec)
	return exec, nil
}

// BuildExecutorQuiet builds an executor while a TUI owns the terminal.
func BuildExecutorQuiet(sess *stadogit.Session, cfg *config.Config, agentName string, metrics telemetry.Metrics) (*tools.Executor, error) {
	var exec *tools.Executor
	var err error
	withRegistryDiagnosticsSuppressed(func() {
		exec, err = BuildExecutor(sess, cfg, agentName, metrics)
	})
	return exec, err
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
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
