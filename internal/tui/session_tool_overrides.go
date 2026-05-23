package tui

import (
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
)

// sessionToolOverrides holds in-memory edits to the [tools] section
// produced by /tool enable/disable/autoload/unautoload slash verbs
// without --save.  effectiveTools merges them with a disk-backed
// config.Config to produce a transient view the runtime can use to
// recompute autoloaded / filtered tool surfaces, without writing
// anything to disk.
//
// Slash mutations with --save bypass this struct entirely and call
// config.WriteToolsList{Add,Remove} directly; the Model's field
// stays at its zero value.
type sessionToolOverrides struct {
	enableAdd      []string
	enableRemove   []string
	disableAdd     []string
	disableRemove  []string
	autoloadAdd    []string
	autoloadRemove []string
}

// effectiveTools produces cfg.Tools as it would appear after
// applying the in-memory overrides.  cfg may be nil; the function
// returns a zero-value Tools populated with only the override-add
// lists in that case.
func (o sessionToolOverrides) effectiveTools(cfg *config.Config) config.Tools {
	var base config.Tools
	if cfg != nil {
		base = cfg.Tools
	}
	// Codex #088: `/tool unautoload bash` on a default-empty config
	// previously did nothing — applyOverride produced an empty
	// Autoload, which runtime.AutoloadedTools then read as "use
	// defaults," autoloading bash again. Materialize the runtime
	// defaults into the override-input here so the remove (or an
	// add) actually takes effect. Only materialize when there's an
	// autoload override AND the base list is empty — otherwise we'd
	// re-introduce defaults the operator had explicitly cleared at
	// the config level.
	autoloadBase := base.Autoload
	if len(autoloadBase) == 0 && (len(o.autoloadAdd) > 0 || len(o.autoloadRemove) > 0) {
		autoloadBase = runtime.DefaultAutoloadNames()
	}
	return config.Tools{
		Enabled:   applyOverride(base.Enabled, o.enableAdd, o.enableRemove),
		Disabled:  applyOverride(base.Disabled, o.disableAdd, o.disableRemove),
		Autoload:  applyOverride(autoloadBase, o.autoloadAdd, o.autoloadRemove),
		Overrides: base.Overrides,
	}
}

// isZero reports whether the override has no recorded mutations.
// Used as a fast-path bypass in Model.effectiveConfig (Task 4) so
// the common no-overrides case avoids allocating a copy.
func (o sessionToolOverrides) isZero() bool {
	return len(o.enableAdd) == 0 && len(o.enableRemove) == 0 &&
		len(o.disableAdd) == 0 && len(o.disableRemove) == 0 &&
		len(o.autoloadAdd) == 0 && len(o.autoloadRemove) == 0
}

// applyOverride returns base ∪ adds \ removes, preserving original
// order and skipping duplicates.
//
// Match semantics for `removes` are canonical-vs-wire aware via
// runtime.ToolMatchesGlob: `/tool unautoload shell.bash` (canonical)
// successfully removes a wire-form `shell__bash` entry from `base`,
// and `/tool unautoload fs.*` removes every `fs__*` / `fs.*` entry.
// Without this, the materialized-default-then-override path produced
// surprising results on canonical operator input — operators typed
// `shell.bash` and watched the default `shell__bash` survive
// (Copilot caught the canonical↔wire form mismatch on #50 round 1).
//
// Duplicates for `adds` are detected by canonical equivalence too:
// adding `shell.bash` when `shell__bash` is already present doesn't
// produce a second entry.
func applyOverride(base, adds, removes []string) []string {
	out := make([]string, 0, len(base)+len(adds))
	matchesRemove := func(s string) bool {
		for _, r := range removes {
			if runtime.ToolMatchesGlob(s, r) {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	canonicalSeen := map[string]bool{}
	recordSeen := func(s string) {
		seen[s] = true
		canonicalSeen[runtime.CanonicalToolName(s)] = true
	}
	alreadySeen := func(s string) bool {
		return seen[s] || canonicalSeen[runtime.CanonicalToolName(s)]
	}
	for _, b := range base {
		if matchesRemove(b) || alreadySeen(b) {
			continue
		}
		recordSeen(b)
		out = append(out, b)
	}
	for _, a := range adds {
		if matchesRemove(a) || alreadySeen(a) {
			continue
		}
		recordSeen(a)
		out = append(out, a)
	}
	return out
}

// effectiveConfig returns a copy of m.cfg with [tools] replaced by
// the override-merged view. Returns m.cfg unchanged when there are
// no overrides — cheap zero-value path.
//
// Used by /tool ls (so the operator sees the live state) and by
// visibleTools (so disabled tools disappear from the model's surface).
func (m *Model) effectiveConfig() *config.Config {
	return m.effectiveConfigFromBase(m.cfg)
}

// effectiveConfigFromBase is the parameterized form of effectiveConfig.
// Callers that just loaded a fresh cfg from disk (e.g. handleToolExecSlash
// after `/tool ... --save` may have written to the on-disk [tools])
// should pass that fresh cfg as the base so the session-override merge
// sits on top of disk reality, not the possibly-stale in-memory m.cfg
// snapshot. Codex P1 caught this on #50 round 1.
func (m *Model) effectiveConfigFromBase(base *config.Config) *config.Config {
	if m == nil || base == nil {
		return nil
	}
	if m.sessionToolOverrides.isZero() {
		return base
	}
	cp := *base
	cp.Tools = m.sessionToolOverrides.effectiveTools(base)
	return &cp
}

// sessionToolOverrideHidesTool reports whether the given tool name
// should be hidden from the model's surface based on session
// overrides. Hidden if (a) it appears in disableAdd and not in
// disableRemove, or (b) it appears in enableRemove (operator pulled
// it out of the live enabled set).
//
// Match semantics are canonical-vs-wire aware and glob-aware via
// [runtime.ToolMatchesGlob], so `disableAdd=["fs.*"]` correctly
// hides `fs__read` and `disableAdd=["shell.bash"]` hides
// `shell__bash`. The prior exact-string equality silently failed
// for those realistic patterns (Copilot caught this on #50 round 1).
//
// Subtractive only — overrides can never widen the executor's
// registry, only narrow it.
func (m *Model) sessionToolOverrideHidesTool(name string) bool {
	o := &m.sessionToolOverrides
	for _, r := range o.disableRemove {
		if runtime.ToolMatchesGlob(name, r) {
			return false // explicitly un-disabled
		}
	}
	for _, d := range o.disableAdd {
		if runtime.ToolMatchesGlob(name, d) {
			return true
		}
	}
	for _, r := range o.enableRemove {
		if runtime.ToolMatchesGlob(name, r) {
			return true
		}
	}
	return false
}
