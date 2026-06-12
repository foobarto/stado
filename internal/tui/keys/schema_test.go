package keys

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestResolveSchema verifies the emacs base is returned intact for the
// default/unknown schema names, and that the vscode delta overlays only the
// actions it names while leaving every other emacs binding untouched.
func TestResolveSchema(t *testing.T) {
	emacs := ResolveSchema("emacs")
	// The emacs schema is the base map as-is.
	for action, want := range Defaults {
		if got := emacs[action]; got != want {
			t.Errorf("emacs schema action %q = %q, want emacs base %q", action, got, want)
		}
	}
	if len(emacs) != len(Defaults) {
		t.Errorf("emacs schema has %d actions, want %d (the emacs base)", len(emacs), len(Defaults))
	}

	// Unknown name and "" both fall back to the emacs base (no error).
	for _, name := range []string{"", "does-not-exist"} {
		got := ResolveSchema(name)
		if got[InputLineHome] != Defaults[InputLineHome] {
			t.Errorf("ResolveSchema(%q) InputLineHome = %q, want emacs base %q", name, got[InputLineHome], Defaults[InputLineHome])
		}
		if len(got) != len(Defaults) {
			t.Errorf("ResolveSchema(%q) has %d actions, want %d (emacs base)", name, len(got), len(Defaults))
		}
	}

	// The vscode delta overlays Home/End on the input line and reassigns
	// history-top/bottom to ctrl+home / ctrl+end.
	vscode := ResolveSchema("vscode")
	deltaWant := map[Action]string{
		InputLineHome: "home",
		InputLineEnd:  "end",
		MessagesFirst: "ctrl+home",
		MessagesLast:  "ctrl+end",
	}
	for action, want := range deltaWant {
		if got := vscode[action]; got != want {
			t.Errorf("vscode schema action %q = %q, want delta %q", action, got, want)
		}
	}
	// Everything NOT in the delta stays emacs-like.
	for action, want := range Defaults {
		if _, overridden := deltaWant[action]; overridden {
			continue
		}
		if got := vscode[action]; got != want {
			t.Errorf("vscode schema action %q = %q, want unchanged emacs base %q", action, got, want)
		}
	}

	// ResolveSchema must not mutate the shared Defaults / Schemas maps.
	if Defaults[InputLineHome] != "ctrl+a" {
		t.Errorf("ResolveSchema mutated the shared Defaults map: InputLineHome = %q", Defaults[InputLineHome])
	}
}

// TestNewRegistryForSchema verifies a registry built for the vscode schema
// resolves its bindings through the schema delta rather than the raw emacs
// base, while NewRegistry() stays on the emacs default.
func TestNewRegistryForSchema(t *testing.T) {
	r := NewRegistryForSchema("vscode")
	// vscode binds InputLineHome to "home".
	homeMsg := tea.KeyPressMsg{Code: tea.KeyHome}
	if !r.Matches(homeMsg, InputLineHome) {
		t.Errorf("vscode registry: expected Home to match InputLineHome")
	}

	// The default registry stays emacs: Home is NOT InputLineHome there.
	def := NewRegistry()
	if def.Matches(homeMsg, InputLineHome) {
		t.Errorf("default (emacs) registry: Home should not match InputLineHome")
	}
	// NewRegistry == NewRegistryForSchema(DefaultSchemaName).
	if DefaultSchemaName != "emacs" {
		t.Errorf("DefaultSchemaName = %q, want emacs", DefaultSchemaName)
	}
}
