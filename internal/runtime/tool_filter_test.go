package runtime

import (
	"sort"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// names returns a sorted slice of tool names for comparison.
func toolNames(r interface {
	All() []interface {
		Name() string
	}
}) []string {
	return nil
}

// TestApplyToolFilter_DefaultKeepsEverything: no config values →
// registry unchanged.
func TestApplyToolFilter_DefaultKeepsEverything(t *testing.T) {
	reg := BuildDefaultRegistry()
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
	reg := BuildDefaultRegistry()
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
	reg := BuildDefaultRegistry()
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
	reg := BuildDefaultRegistry()
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
	reg := BuildDefaultRegistry()
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"nopes-not-a-real-tool"}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("unknown tool name should be a no-op; was %d, got %d", before, got)
	}
}
