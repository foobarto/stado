package runtime

import (
	"reflect"
	"testing"
)

func TestDisplayPluginName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"stado-builtin-tool-fs", "fs"},
		{"stado-builtin-tool-shell", "shell"},
		{"gtfobins", "gtfobins"},
		{"htb-toolkit", "htb-toolkit"},
		{"", ""},
	}
	for _, c := range cases {
		if got := displayPluginName(c.in); got != c.want {
			t.Errorf("displayPluginName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAutoloadedPluginNames_DefaultRegistry exercises the helper end
// to end against the bundled plugin set BuildDefaultRegistry stamps
// out, so the test catches regressions in the wrapping chain
// (renamedTool → bundledPluginTool.PluginName) without mocking it.
// Asserts the names every release ships, not the full set, so adding
// a new bundled plugin doesn't force this test to update.
func TestAutoloadedPluginNames_DefaultRegistry(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	got := AutoloadedPluginNames(reg, nil)

	wantSubset := []string{"agent", "fs", "shell"}
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, w := range wantSubset {
		if !gotSet[w] {
			t.Errorf("expected %q in autoloaded plugin names; got %v", w, got)
		}
	}
	// Bundled-plugin prefix must not leak — it's the operator-facing
	// hint line on the landing screen, the manifest internal name
	// would just be noise.
	for _, n := range got {
		if reflect.TypeOf(n).Kind() != reflect.String {
			t.Fatalf("non-string entry %v", n)
		}
		if len(n) > 20 && n[:20] == "stado-builtin-tool-s" {
			t.Errorf("bundled prefix leaked into display name: %q", n)
		}
	}
}

func TestAutoloadedPluginNames_NilRegistry(t *testing.T) {
	if got := AutoloadedPluginNames(nil, nil); got != nil {
		t.Errorf("AutoloadedPluginNames(nil, nil) = %v, want nil", got)
	}
}
