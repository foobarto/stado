package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

// #21 step 2: the sidebar renders only the configured sections, in order.
func TestSidebarRespectsConfiguredSections(t *testing.T) {
	m := scenarioModel(t)
	m.height = 24
	m.blocks = []block{{kind: "tool", toolName: "bash"}}

	m.sidebarSections = []string{"repo", "now"} // repo first, hide the rest
	out := ansi.Strip(m.renderSidebar(34))

	if !strings.Contains(out, "Repo") || !strings.Contains(out, "Now") {
		t.Fatalf("configured sections should render:\n%s", out)
	}
	if strings.Contains(out, "Agent") {
		t.Errorf("Agent should be hidden when not in the section list:\n%s", out)
	}
	if strings.Index(out, "Repo") > strings.Index(out, "Now") {
		t.Errorf("Repo should render before Now per the configured order:\n%s", out)
	}
}

// #21 step 3: the footer shows only the configured segments.
func TestFooterRespectsConfiguredSegments(t *testing.T) {
	m := scenarioModel(t)
	m.usage.InputTokens = 1200
	m.usage.CostUSD = 0.05

	def := ansi.Strip(m.renderStatus(80))
	if !strings.Contains(def, "$0.05") || !strings.Contains(def, "commands") {
		t.Fatalf("default footer should show cost + commands:\n%s", def)
	}

	m.footerSegments = []string{"state", "tokens", "persona"} // hide cost, daemon, commands
	out := ansi.Strip(m.renderStatus(80))
	if strings.Contains(out, "$0.05") {
		t.Errorf("cost should be hidden when not in segments:\n%s", out)
	}
	if strings.Contains(out, "commands") {
		t.Errorf("commands should be hidden when not in segments:\n%s", out)
	}
	if !strings.Contains(out, "1.2K") {
		t.Errorf("tokens should still show:\n%s", out)
	}
}
