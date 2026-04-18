package palette

import (
	"testing"
)

func TestSlashUpdateFilter(t *testing.T) {
	m := New()

	m.UpdateFilter("test")
	if m.Visible {
		t.Errorf("Expected not visible without prefix /")
	}

	m.UpdateFilter("/")
	if !m.Visible || len(m.Matches) != len(Commands) {
		t.Errorf("Expected visible and all matches on exact /")
	}

	m.UpdateFilter("/he")
	if len(m.Matches) == 0 || m.Matches[0].Name != "/help" {
		t.Errorf("Expected /help to be top match for /he")
	}
}

func TestSlashSelected(t *testing.T) {
	m := New()

	m.UpdateFilter("/")
	if m.Selected() == nil || m.Selected().Name != Commands[0].Name {
		t.Errorf("Expected %s as default selected", Commands[0].Name)
	}
}
