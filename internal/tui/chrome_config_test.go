package tui

import (
	"reflect"
	"testing"
)

func TestNormalizeChromeList(t *testing.T) {
	// Empty / absent → defaults (the #21 decision).
	if got := normalizeSidebarSections(nil); !reflect.DeepEqual(got, defaultSidebarSections) {
		t.Errorf("nil sidebar should use defaults, got %v", got)
	}
	if got := normalizeFooterSegments([]string{}); !reflect.DeepEqual(got, defaultFooterSegments) {
		t.Errorf("empty footer should use defaults, got %v", got)
	}

	// Reorder + dedup; unknown ids (plugin panel ids) are preserved, blanks dropped.
	got := normalizeSidebarSections([]string{"agent", "now", "agent", "", "my-plugin"})
	want := []string{"agent", "now", "my-plugin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// A list that's all blanks collapses to nothing → defaults.
	if got := normalizeSidebarSections([]string{"", ""}); !reflect.DeepEqual(got, defaultSidebarSections) {
		t.Errorf("all-blank should fall back to defaults, got %v", got)
	}
}
