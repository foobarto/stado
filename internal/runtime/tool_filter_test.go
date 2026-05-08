package runtime

import (
	"sort"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	pkgtoolPkg "github.com/foobarto/stado/pkg/tool"
)

// TestApplyToolFilter_DefaultKeepsEverything: no config values →
// registry unchanged.
func TestApplyToolFilter_DefaultKeepsEverything(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("no-config filter shouldn't change count; was %d, got %d", before, got)
	}
}

// TestApplyToolFilter_EnabledAllowlist: only the listed tools
// survive.
func TestApplyToolFilter_EnabledAllowlist(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs__read", "fs__grep"}
	ApplyToolFilter(reg, cfg)

	var names []string
	for _, tl := range reg.All() {
		names = append(names, tl.Name())
	}
	sort.Strings(names)
	want := []string{"fs__grep", "fs__read"}
	if len(names) != 2 {
		t.Fatalf("expected 2 tools (read+grep), got %d: %v", len(names), names)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("names[%d] = %q, want %q", i, names[i], n)
		}
	}
}

// TestApplyToolFilter_DisabledRemovesNamed: disabled removes only
// the listed tools; all others remain.
func TestApplyToolFilter_DisabledRemovesNamed(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"shell__bash", "web__fetch"}
	ApplyToolFilter(reg, cfg)

	after := len(reg.All())
	if after != before-2 {
		t.Errorf("disabled should trim 2 tools; was %d → %d", before, after)
	}
	for _, tl := range reg.All() {
		if tl.Name() == "shell__bash" || tl.Name() == "web__fetch" {
			t.Errorf("tool %q should have been removed", tl.Name())
		}
	}
}

// TestApplyToolFilter_EnabledWinsOverDisabled: when both are set,
// Enabled is authoritative (allowlist) and Disabled is ignored.
func TestApplyToolFilter_EnabledWinsOverDisabled(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs__read"}
	cfg.Tools.Disabled = []string{"fs__read"} // would conflict if honoured
	ApplyToolFilter(reg, cfg)

	tools := reg.All()
	if len(tools) != 1 || tools[0].Name() != "fs__read" {
		t.Errorf("Enabled allowlist should win; got %v", tools)
	}
}

// TestApplyToolFilter_UnknownNamesTolerated: a typo in either list
// shouldn't panic or remove anything unexpected.
func TestApplyToolFilter_UnknownNamesTolerated(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"nopes-not-a-real-tool"}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("unknown tool name should be a no-op; was %d, got %d", before, got)
	}
}

// TestApplyToolFilter_EmptyAllowFailsClosed: when [tools].enabled is
// non-empty but matches zero registered tools, the registry is emptied
// (not left untouched). Operator typos / refs to uninstalled tools
// shouldn't silently re-expose the entire tool surface — that defeats
// the whole point of an allowlist. The filter prints a stderr advisory
// naming the unmatched patterns and unregisters everything. Replaces
// the prior TestApplyToolFilter_UnknownEnabledNamesDoNotEmptyRegistry,
// which asserted the buggy fall-open behaviour as if it were intent.
func TestApplyToolFilter_EmptyAllowFailsClosed(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	if len(reg.All()) == 0 {
		t.Fatal("default registry should contain tools; got empty")
	}
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"renamed-tool", "missing-tool"}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != 0 {
		t.Fatalf("unmatched [tools].enabled should fail closed (empty registry); got %d tools", got)
	}
}

// TestApplyToolFilter_CanonicalNameMatchesWireForm: an operator
// configuring [tools].enabled = ["fs.read"] reasonably expects the
// registered wire-form tool fs__read to survive the filter and other
// tools to be dropped. Before the canonical-vs-wire match in
// ToolMatchesGlob, exact-canonical patterns silently failed to match
// any wire-form name, the empty-allow fall-open then left every tool
// enabled, and the operator's allowlist was effectively a no-op.
// Lock the right behaviour in.
func TestApplyToolFilter_CanonicalNameMatchesWireForm(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs.read"}
	ApplyToolFilter(reg, cfg)
	if _, ok := reg.Get("fs__read"); !ok {
		t.Errorf("fs__read should survive [tools].enabled=[fs.read] (canonical → wire match)")
	}
	for _, tl := range reg.All() {
		if tl.Name() != "fs__read" {
			t.Errorf("tool %q should have been filtered out by [fs.read] allowlist", tl.Name())
		}
	}
}

func TestToolMatchesGlob(t *testing.T) {
	cases := []struct {
		name, pat string
		want      bool
	}{
		// Exact bare-name match (pre-EP-0038 tools)
		{"fs__read", "fs__read", true},
		{"bash", "bash", true},
		{"webfetch", "web.*", false}, // bare "webfetch" has no __ separator, doesn't match "web.*"
		// Wire-form with dotted glob
		{"fs__read", "fs.*", true},
		{"fs__write", "fs.*", true},
		{"shell__exec", "fs.*", false},
		{"tools__search", "tools.*", true},
		{"tools__describe", "tools.*", true},
		// Universal wildcard
		{"fs__read", "*", true},
		{"fs__read", "*", true},
		// No match
		{"web__fetch", "fs.*", false},
		// Canonical-form pattern against wire-form registered name —
		// what an operator types in [tools].enabled / --tools.
		{"fs__read", "fs.read", true},
		{"shell__bash", "shell.bash", true},
		{"fs__read", "shell.bash", false},
		// Canonical-with-dash: alias "htb-lab" → wire "htb_lab"
		{"htb_lab__spawn", "htb-lab.spawn", false}, // exact canonical doesn't normalise dashes; pattern must use the wire-segment form. Documented behaviour.
		// Empty inputs.
		{"", "", true},
		{"fs__read", "", false},
	}
	for _, c := range cases {
		got := ToolMatchesGlob(c.name, c.pat)
		if got != c.want {
			t.Errorf("ToolMatchesGlob(%q, %q) = %v, want %v", c.name, c.pat, got, c.want)
		}
	}
}

func TestAutoloadedTools_DefaultCore(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{} // empty — use hardcoded defaults
	autoloaded := AutoloadedTools(reg, cfg)

	names := map[string]bool{}
	for _, tl := range autoloaded {
		names[tl.Name()] = true
	}
	// Default convenience tools must be present. Step 4 of EP-no-internal-
	// tools renamed bare 'bash' to wire-form 'shell__bash'.
	for _, want := range []string{"fs__read", "fs__write", "fs__edit", "fs__glob", "fs__grep", "shell__bash"} {
		if !names[want] {
			t.Errorf("default autoload should include %q", want)
		}
	}
	// Meta-tools always present regardless of config.
	for _, want := range []string{"tools__search", "tools__describe", "tools__categories", "tools__in_category"} {
		if !names[want] {
			t.Errorf("meta-tool %q should always be autoloaded", want)
		}
	}
}

// TestAutoloadedTools_CategoriesAddTools: AutoloadCategories pulls
// tools whose Categories metadata overlaps; layered on top of the
// name-based autoload (union, deduped).
func TestAutoloadedTools_CategoriesAddTools(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.AutoloadCategories = []string{"file"}
	autoloaded := AutoloadedTools(reg, cfg)
	// The bundled fs tools are tagged "file"; verify some of them
	// surface even without an explicit name-based autoload.
	want := []string{"fs__read", "fs__write"}
	got := map[string]bool{}
	for _, t := range autoloaded {
		got[t.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("autoload-category=file should pull %q; got: %v", name, listNames(autoloaded))
		}
	}
}

// listNames is a small helper to render a tool slice as a sorted
// comma-joined string for error messages.
func listNames(ts []pkgtoolPkg.Tool) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name())
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestAutoloadedTools_CustomAutoload(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs__read", "fs__grep"}
	autoloaded := AutoloadedTools(reg, cfg)

	names := map[string]bool{}
	for _, tl := range autoloaded {
		names[tl.Name()] = true
	}
	if !names["fs__read"] || !names["fs__grep"] {
		t.Error("custom autoload should include read and grep")
	}
	if names["bash"] {
		t.Error("bash should NOT be autoloaded when not in custom autoload list")
	}
	// Meta-tools still always present.
	if !names["tools__search"] {
		t.Error("tools__search should always be autoloaded")
	}
}

func TestApplyToolFilter_WildcardDisabled(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"bash"} // exact name
	ApplyToolFilter(reg, cfg)
	for _, tl := range reg.All() {
		if tl.Name() == "bash" {
			t.Error("bash should be removed by disabled list")
		}
	}
}

func TestApplyToolFilter_GlobDisabled(t *testing.T) {
	// After EP-0038 tools get wire names, this tests glob removal.
	// For now just verify zero-match glob is a silent no-op.
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"nonexistent.*"} // glob matching nothing
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("zero-match glob disable should be no-op; was %d, got %d", before, got)
	}
}
