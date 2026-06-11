package tui

// Configurable sidebar/footer layout (#21). The operator picks the layout via
// [tui.sidebar].sections (which sections show AND in what order) and
// [tui.footer].segments (which footer segments are VISIBLE — the footer's order
// is fixed by its template). An empty/absent list means "use the default
// layout" (NOT "hide everything"); to hide a section, list the ones to keep.
//
// Sidebar section ids may be built-ins (below) OR a plugin-contributed panel
// id (once the plugin display API lands); an id that maps to nothing renders
// nothing.

// defaultSidebarSections is the built-in sidebar layout, top to bottom.
var defaultSidebarSections = []string{
	"header", "now", "subagents", "risk", "agent", "repo", "logs", "todos",
}

// defaultFooterSegments is the built-in footer right-cluster layout.
var defaultFooterSegments = []string{
	"state", "tokens", "cost", "cache", "budget", "persona", "daemon", "commands",
}

// normalizeChromeList dedups a configured section/segment list, preserving
// order. An empty result falls back to the supplied default (empty == defaults,
// per the #21 decision). Unknown ids are kept — they may be plugin panel ids
// resolved at render time.
func normalizeChromeList(in, def []string) []string {
	if len(in) == 0 {
		return def
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func normalizeSidebarSections(in []string) []string {
	return normalizeChromeList(in, defaultSidebarSections)
}

// effectiveSidebarSections returns the configured section order, defaulting
// when unset (e.g. a test model built without going through app.go).
func (m *Model) effectiveSidebarSections() []string {
	if len(m.sidebarSections) == 0 {
		return defaultSidebarSections
	}
	return m.sidebarSections
}

func normalizeFooterSegments(in []string) []string {
	return normalizeChromeList(in, defaultFooterSegments)
}

// effectiveFooterSegments returns the configured footer segments, defaulting
// when unset (e.g. a test model built without going through app.go).
func (m *Model) effectiveFooterSegments() []string {
	if len(m.footerSegments) == 0 {
		return defaultFooterSegments
	}
	return m.footerSegments
}
