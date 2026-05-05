package tui

import (
	"github.com/foobarto/stado/internal/config"
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
	return config.Tools{
		Enabled:   applyOverride(base.Enabled, o.enableAdd, o.enableRemove),
		Disabled:  applyOverride(base.Disabled, o.disableAdd, o.disableRemove),
		Autoload:  applyOverride(base.Autoload, o.autoloadAdd, o.autoloadRemove),
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
func applyOverride(base, adds, removes []string) []string {
	out := make([]string, 0, len(base)+len(adds))
	skip := map[string]bool{}
	for _, r := range removes {
		skip[r] = true
	}
	seen := map[string]bool{}
	for _, b := range base {
		if skip[b] || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	for _, a := range adds {
		if skip[a] || seen[a] {
			continue
		}
		seen[a] = true
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
	if m == nil || m.cfg == nil {
		return nil
	}
	if m.sessionToolOverrides.isZero() {
		return m.cfg
	}
	cp := *m.cfg
	cp.Tools = m.sessionToolOverrides.effectiveTools(m.cfg)
	return &cp
}

// sessionToolOverrideHidesTool reports whether the given tool name
// should be hidden from the model's surface based on session
// overrides. Hidden if (a) it appears in disableAdd and not in
// disableRemove, or (b) it appears in enableRemove (operator pulled
// it out of the live enabled set).
//
// Subtractive only — overrides can never widen the executor's
// registry, only narrow it.
func (m *Model) sessionToolOverrideHidesTool(name string) bool {
	o := &m.sessionToolOverrides
	for _, r := range o.disableRemove {
		if r == name {
			return false // explicitly un-disabled
		}
	}
	for _, d := range o.disableAdd {
		if d == name {
			return true
		}
	}
	for _, r := range o.enableRemove {
		if r == name {
			return true
		}
	}
	return false
}
