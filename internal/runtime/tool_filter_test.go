package runtime

import (
	"sort"
	"testing"

	"github.com/foobarto/stado/internal/config"
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
	cfg.Tools.Enabled = []string{"read", "grep"}
	ApplyToolFilter(reg, cfg)

	var names []string
	for _, tl := range reg.All() {
		names = append(names, tl.Name())
	}
	sort.Strings(names)
	want := []string{"grep", "read"}
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
	cfg.Tools.Disabled = []string{"bash", "webfetch"}
	ApplyToolFilter(reg, cfg)

	after := len(reg.All())
	if after != before-2 {
		t.Errorf("disabled should trim 2 tools; was %d → %d", before, after)
	}
	for _, tl := range reg.All() {
		if tl.Name() == "bash" || tl.Name() == "webfetch" {
			t.Errorf("tool %q should have been removed", tl.Name())
		}
	}
}

// TestApplyToolFilter_EnabledWinsOverDisabled: when both are set,
// Enabled is authoritative (allowlist) and Disabled is ignored.
func TestApplyToolFilter_EnabledWinsOverDisabled(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"read"}
	cfg.Tools.Disabled = []string{"read"} // would conflict if honoured
	ApplyToolFilter(reg, cfg)

	tools := reg.All()
	if len(tools) != 1 || tools[0].Name() != "read" {
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

func TestApplyToolFilter_UnknownEnabledNamesDoNotEmptyRegistry(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"renamed-tool", "missing-tool"}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Fatalf("unknown enabled tools should be ignored; was %d, got %d", before, got)
	}
}

func TestToolMatchesGlob(t *testing.T) {
	cases := []struct {
		name, pat string
		want      bool
	}{
		// Exact bare-name match (pre-EP-0038 tools)
		{"read", "read", true},
		{"bash", "bash", true},
		{"webfetch", "web.*", false}, // bare "webfetch" has no __ separator, doesn't match "web.*"
		// Wire-form with dotted glob
		{"fs__read", "fs.*", true},
		{"fs__write", "fs.*", true},
		{"shell__exec", "fs.*", false},
		{"tools__search", "tools.*", true},
		{"tools__describe", "tools.*", true},
		// Universal wildcard
		{"read", "*", true},
		{"fs__read", "*", true},
		// No match
		{"web__fetch", "fs.*", false},
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
	// Default convenience tools must be present.
	for _, want := range []string{"read", "write", "edit", "glob", "grep", "bash"} {
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

func TestAutoloadedTools_CustomAutoload(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"read", "grep"}
	autoloaded := AutoloadedTools(reg, cfg)

	names := map[string]bool{}
	for _, tl := range autoloaded {
		names[tl.Name()] = true
	}
	if !names["read"] || !names["grep"] {
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
