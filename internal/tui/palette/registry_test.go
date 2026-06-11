package palette

import (
	"strings"
	"testing"
)

// resetDynamic clears the package-global dynamic layer so each test
// starts from the built-in-only baseline regardless of order.
func resetDynamic(t *testing.T) {
	t.Helper()
	RegisterDynamicCommands(nil)
	t.Cleanup(func() { RegisterDynamicCommands(nil) })
}

func TestRegisterDynamicCommands_MergesIntoAll(t *testing.T) {
	resetDynamic(t)

	base := len(Commands)
	if got := len(allCommands()); got != base {
		t.Fatalf("with no dynamic commands, allCommands()=%d, want %d (built-ins only)", got, base)
	}

	RegisterDynamicCommands([]Command{
		{Name: "/refactor", Desc: "Extract a helper", Group: "Skills"},
		{Name: "/review", Desc: "Review the diff", Group: "Skills"},
	})

	merged := allCommands()
	if len(merged) != base+2 {
		t.Fatalf("merged length = %d, want %d", len(merged), base+2)
	}
	// Built-ins come first; dynamic commands are appended.
	if merged[0].Name != Commands[0].Name {
		t.Errorf("first merged command = %q, want built-in %q", merged[0].Name, Commands[0].Name)
	}
	if merged[len(merged)-1].Name != "/review" {
		t.Errorf("last merged command = %q, want /review", merged[len(merged)-1].Name)
	}
}

func TestRegisterDynamicCommands_ReplacesNotAppends(t *testing.T) {
	resetDynamic(t)

	RegisterDynamicCommands([]Command{{Name: "/a", Group: "Skills"}})
	RegisterDynamicCommands([]Command{{Name: "/b", Group: "Skills"}})

	dyn := DynamicCommands()
	if len(dyn) != 1 || dyn[0].Name != "/b" {
		t.Fatalf("dynamic layer = %+v, want a single /b (replace, not append)", dyn)
	}
}

func TestRegisterDynamicCommands_NilClears(t *testing.T) {
	resetDynamic(t)
	RegisterDynamicCommands([]Command{{Name: "/a"}})
	RegisterDynamicCommands(nil)
	if got := len(DynamicCommands()); got != 0 {
		t.Fatalf("nil register should clear dynamic layer, got %d", got)
	}
	if got := len(allCommands()); got != len(Commands) {
		t.Fatalf("after clear, allCommands()=%d, want built-ins %d", got, len(Commands))
	}
}

func TestCheckSlashCollision_RejectsBuiltins(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"/help", true},      // built-in palette row, with slash
		{"help", true},       // same, without slash
		{"/model", true},     // built-in
		{"/reload", true},    // built-in
		{"refactor", false},  // novel skill name
		{"/refactor", false}, // novel, with slash
		{"", false},          // empty never collides
		{"helpme", false},    // superstring of a built-in, no collision
	}
	for _, tc := range cases {
		if got := CheckSlashCollision(tc.name); got != tc.want {
			t.Errorf("CheckSlashCollision(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDynamicCommandAppearsInMatches proves a registered dynamic command
// is reachable through the SAME match list both surfaces (View / InlineView)
// read — refresh() consults allCommands().
func TestDynamicCommandAppearsInMatches(t *testing.T) {
	resetDynamic(t)
	RegisterDynamicCommands([]Command{
		{Name: "/refactor", Desc: "Extract a helper", Group: "Skills"},
	})

	m := New()
	m.Open()
	// Empty query → all commands, including the dynamic one.
	found := false
	for _, c := range m.Matches {
		if c.Name == "/refactor" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("dynamic command /refactor not in match list (browse mode)")
	}

	// Fuzzy filter should surface it too.
	m.Query = "refac"
	m.refresh()
	if len(m.Matches) == 0 || !strings.Contains(m.Matches[0].Name, "refactor") {
		t.Fatalf("filtered matches = %+v, want /refactor on top", m.Matches)
	}
}
