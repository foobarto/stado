package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/agent"
	pkgtool "github.com/foobarto/stado/pkg/tool"
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
	// Codex C4/Q P2: bind cfg + the final composed registry onto every
	// bundled tool that was registered above. Pre-fix the bundled
	// pluginTool.Run path read process-wide globals
	// (installedRunCfg / installedInvokeReg) that got REBOUND by every
	// subsequent BuildDefaultRegistry call — e.g. a /tool info build's
	// unfiltered registry leaked into the in-flight session's bundled
	// dispatch surface, silently widening it past [tools].enabled. Now
	// each build's bundled tools are anchored to their own build's
	// registry pointer + cfg, so independent builds don't interfere.
	// Unwrap renamedTool wrappers — bundled wasm tools registered via
	// newBundledWasmTool are wrapped in *renamedTool{inner: *bundledPluginTool}
	// to expose the wire-form name. A direct type-assert misses those
	// and leaves the inner fields nil; walk past the wrapper.
	for _, t := range reg.All() {
		inner := t
		if rt, ok := inner.(*renamedTool); ok {
			inner = rt.inner
		}
		if b, ok := inner.(*bundledPluginTool); ok {
			b.setRuntime(cfg, reg)
		}
	}
	return reg
}

// ToolMatchesGlob reports whether a registered tool name matches a config
// pattern. Patterns are either exact names (bare, wire-form, or canonical
// dotted) or wildcard globs:
//
//   - "read"     — exact bare-name match
//   - "fs.read"  — exact canonical (dotted) match against a wire-form name
//   - "fs__read" — exact wire-form match
//   - "fs.*"     — matches any wire-form tool whose alias segment is "fs"
//     (fs__read, fs__write, etc.) or canonical form fs.read
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
	// Dotted wildcard: "fs.*" matches wire-form tools with alias "fs__"
	// and canonical-form tools with prefix "fs.".
	if rest, ok := strings.CutSuffix(pattern, ".*"); ok {
		wirePrefix := tools.WireSegment(rest) + "__"
		dotPrefix := rest + "."
		return strings.HasPrefix(toolName, wirePrefix) || strings.HasPrefix(toolName, dotPrefix)
	}
	// Legacy bare-name pattern (pre-EP-0038): translate the operator's old
	// name to its canonical and re-match, so [tools].disabled=["webfetch"]
	// (etc.) still hides the wasm tool that replaced it instead of being a
	// silent no-op. The canonical is never itself a legacy bare name, so this
	// recurses at most once.
	if canonical, ok := legacyFilterCanonical(pattern); ok {
		return ToolMatchesGlob(toolName, canonical)
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
		if IsMetaTool(t.Name()) {
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
		// Category-based autoload (Tester #7). Tools whose category
		// metadata overlaps with cfg.Tools.AutoloadCategories join the
		// per-turn surface. Empty AutoloadCategories = no category-based
		// expansion. Categories live in tool_metadata.go (per EP-0037 §C
		// the manifest is authoritative; bundled tools mirror their
		// declarations there).
		if len(categorySet) > 0 {
			for _, c := range LookupToolMetadata(t.Name()).Categories {
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

// IsMetaTool reports whether name is one of the dispatch kernel tools.
// All meta-tools are unconditionally autoloaded — they're how the model
// discovers and activates the rest of the surface. Exported so the CLI
// `tool run` path can natively dispatch meta-tools (no WASM backing).
func IsMetaTool(name string) bool {
	switch name {
	case "tools__search", "tools__describe", "tools__categories", "tools__in_category",
		"tools__activate", "tools__deactivate", "plugin__load", "plugin__unload":
		return true
	}
	return false
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
			if strings.ContainsAny(n, "*?") {
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
			fmt.Fprintf(os.Stderr, "stado: [tools].%s mentions %q — no such tool (ignored)\n", label, n)
		}
	}
	warnUnknownExact(cfg.Tools.Enabled, "enabled")
	warnUnknownExact(cfg.Tools.Disabled, "disabled")

	// EP-0037 §E: the meta-tool dispatch kernel (tools.search/describe/
	// categories/in_category + tools.activate/deactivate + plugin.load/unload)
	// is NON-DISABLEABLE. Unregistering it would leave the model unable to
	// discover or activate any non-autoloaded tool — a silent footgun. We never
	// unregister a meta-tool below, and warn loudly when the operator's
	// enabled/disabled lists would have removed one.
	for name := range known {
		if !IsMetaTool(name) {
			continue
		}
		disabledHit := toolMatchesAny(name, cfg.Tools.Disabled)
		allowMiss := len(cfg.Tools.Enabled) > 0 && !toolMatchesAny(name, cfg.Tools.Enabled)
		if disabledHit || allowMiss {
			fmt.Fprintf(os.Stderr,
				"stado: [tools] would remove meta-tool %q, but the dispatch kernel is non-disableable (EP-0037) — keeping it.\n",
				name)
		}
	}

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		nonMetaMatches := 0
		for name := range known {
			if IsMetaTool(name) {
				allow[name] = true // kernel always allowed
				continue
			}
			if toolMatchesAny(name, cfg.Tools.Enabled) {
				allow[name] = true
				nonMetaMatches++
			}
		}
		// Fail closed when the operator's allowlist matches no real (non-meta)
		// tool. The previous fall-open ("return without filtering") defeated the
		// allowlist: typos / uninstalled references silently re-exposed the
		// whole surface. An empty match is more likely operator error than
		// intent — surface it loudly and remove every non-kernel tool so the
		// next run fails fast with "no tools available" rather than
		// "everything enabled". The kernel is retained either way.
		if nonMetaMatches == 0 {
			fmt.Fprintf(os.Stderr,
				"stado: [tools].enabled = %v matched no registered tools — registry reduced to the meta-tool kernel (fail-closed). Did you mean canonical names like \"fs.read\" or globs like \"fs.*\"?\n",
				cfg.Tools.Enabled)
			for name := range known {
				if IsMetaTool(name) {
					continue
				}
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
		// is correctly removed even though fs.* allowed it. Meta-tools
		// are exempt (kernel is non-disableable).
		for name := range known {
			if !allow[name] || IsMetaTool(name) {
				continue
			}
			if toolMatchesAny(name, cfg.Tools.Disabled) {
				reg.Unregister(name)
			}
		}
		return
	}
	// Disabled-only path. Meta-tools are exempt (kernel is non-disableable).
	for name := range known {
		if IsMetaTool(name) {
			continue
		}
		if toolMatchesAny(name, cfg.Tools.Disabled) {
			reg.Unregister(name)
		}
	}
}

// BuildRegistryWithPlugins builds the tool registry the agent loop,
// MCP server, and any other tool-dispatching surface should share.
// Composes:
//
//  1. BuildDefaultRegistry — bundled + installed plugin tools + meta-tools
//  2. tasks tool (the bootstrapping carve-out; migrating to a wasm
//     plugin in Step 8 of EP-no-internal-tools)
//  3. external MCP-attached tools (when cfg.MCP.Servers non-empty)
//  4. ApplyWasmMigration (legacy native↔wasm flip; deletes in Step 9)
//  5. ApplyToolOverrides (cfg.Tools.Overrides → pluginOverrideTool)
//  6. ApplyToolFilter (cfg.Tools.Enabled / Disabled allowlist)
//
// EXCLUDES the MCP-server-only `llm.invoke` tool — mcp_server.go
// registers that on top of the returned registry. The agent and CLI
// surfaces deliberately don't expose llm.invoke (model uses
// stado_agent_* for sub-LLM delegation).
//
// Pre-Step-0.5 the MCP server bypassed this composition, building
// only `BuildDefaultRegistry + tasks + llm.invoke + ApplyToolFilter`
// — missing MCP attach + wasm migration + tool overrides. After this
// helper exists, both BuildExecutor and the MCP server call it for
// uniform tool surface across paths.
func BuildRegistryWithPlugins(cfg *config.Config) (*tools.Registry, error) {
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
	return reg, nil
}

// BuildExecutor wires the shared registry + session + sandbox runner.
//
// Respects cfg.Tools.Enabled / Disabled — the user's allowlist /
// blocklist is applied via BuildRegistryWithPlugins.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string) (*tools.Executor, error) {
	reg, err := BuildRegistryWithPlugins(cfg)
	if err != nil {
		return nil, err
	}
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
