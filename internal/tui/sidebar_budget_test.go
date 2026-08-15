package tui

import (
	"strings"
	"testing"
)

// TestSidebarBudgetLine_TokenOnly (A4 sidebar sibling): the always-on sidebar
// budget gauge must render the binding token cap.
func TestSidebarBudgetLine_TokenOnly(t *testing.T) {
	t.Run("combined tokens", func(t *testing.T) {
		m := &Model{}
		m.budgetWarnTokens = 8000
		m.budgetHardTokens = 10000
		m.cumulativeInputTokens = 9000 // -> totalTokens 9000, past the warn cap
		line := m.sidebarBudgetLine()
		if line.Text == "" {
			t.Fatal("token-only budget produced no sidebar budget line")
		}
		if !strings.Contains(line.Text, "budget") || !strings.Contains(line.Text, "tok") {
			t.Fatalf("sidebar budget line missing token info: %q", line.Text)
		}
		if line.Tone != "warning" {
			t.Fatalf("past the warn cap, tone = %q, want warning", line.Tone)
		}
	})

	t.Run("input tokens only", func(t *testing.T) {
		m := &Model{}
		m.budgetHardInputTokens = 5000
		m.usage.InputTokens = 1000
		line := m.sidebarBudgetLine()
		if line.Text == "" || !strings.Contains(line.Text, "tok") {
			t.Fatalf("input-token budget produced no usable sidebar line: %q", line.Text)
		}
	})

	t.Run("no budget stays empty", func(t *testing.T) {
		m := &Model{}
		if line := m.sidebarBudgetLine(); line.Text != "" {
			t.Fatalf("no budget should produce an empty sidebar line, got %q", line.Text)
		}
	})

}
