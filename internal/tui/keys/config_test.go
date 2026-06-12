package keys

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/config"
)

// TestLoadOverrides verifies a [keymap.bindings] entry REPLACES the named
// action's binding in the registry, and that an unknown action name is
// surfaced as an error (skip-with-error policy) without aborting the
// remaining valid overrides.
func TestLoadOverrides(t *testing.T) {
	r := NewRegistry()

	// Sanity: by default SidebarToggle is ctrl+t, not ctrl+y.
	ctrlY := tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	if r.Matches(ctrlY, SidebarToggle) {
		t.Fatalf("precondition: ctrl+y should not match SidebarToggle before override")
	}

	cfg := &config.Config{
		Keymap: config.Keymap{
			Bindings: map[string]string{
				string(SidebarToggle): "ctrl+y",
			},
		},
	}
	if err := LoadOverrides(r, cfg); err != nil {
		t.Fatalf("LoadOverrides with a valid binding returned error: %v", err)
	}

	// The override REPLACES the binding: ctrl+y now matches, ctrl+t no longer.
	if !r.Matches(ctrlY, SidebarToggle) {
		t.Errorf("after override, ctrl+y should match SidebarToggle")
	}
	ctrlT := tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	if r.Matches(ctrlT, SidebarToggle) {
		t.Errorf("after override, ctrl+t should NO LONGER match SidebarToggle (override replaces)")
	}
}

// TestLoadOverrides_MultiKeyCSV verifies a comma-separated override binds all
// of its keys to the action.
func TestLoadOverrides_MultiKeyCSV(t *testing.T) {
	r := NewRegistry()
	cfg := &config.Config{
		Keymap: config.Keymap{
			Bindings: map[string]string{
				string(SidebarToggle): "ctrl+y,ctrl+o",
			},
		},
	}
	if err := LoadOverrides(r, cfg); err != nil {
		t.Fatalf("LoadOverrides returned error: %v", err)
	}
	if !r.Matches(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}, SidebarToggle) {
		t.Errorf("ctrl+y should match SidebarToggle after multi-key override")
	}
	if !r.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, SidebarToggle) {
		t.Errorf("ctrl+o should match SidebarToggle after multi-key override")
	}
}

// TestLoadOverrides_UnknownAction verifies the skip-with-error policy: an
// unknown action name is reported via the returned error, but valid bindings
// in the same map still apply.
func TestLoadOverrides_UnknownAction(t *testing.T) {
	r := NewRegistry()
	cfg := &config.Config{
		Keymap: config.Keymap{
			Bindings: map[string]string{
				"not_a_real_action":   "ctrl+y",
				string(SidebarToggle): "ctrl+y",
			},
		},
	}
	err := LoadOverrides(r, cfg)
	if err == nil {
		t.Fatalf("LoadOverrides with an unknown action name should return an error")
	}
	// The valid override still applied despite the unknown one.
	if !r.Matches(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}, SidebarToggle) {
		t.Errorf("valid override should still apply alongside an unknown-action error")
	}
}

// TestLoadOverrides_NilOrEmpty verifies a nil config or empty bindings map is
// a no-op with no error.
func TestLoadOverrides_NilOrEmpty(t *testing.T) {
	r := NewRegistry()
	if err := LoadOverrides(r, nil); err != nil {
		t.Errorf("LoadOverrides(nil cfg) should be a no-op, got error: %v", err)
	}
	if err := LoadOverrides(r, &config.Config{}); err != nil {
		t.Errorf("LoadOverrides(empty cfg) should be a no-op, got error: %v", err)
	}
}
