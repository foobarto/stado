package tui

import (
	"strings"
	"testing"
)

// TestSidebarBudgetLine_TokenOnly (A4 sidebar sibling): the always-on sidebar
// budget gauge was USD-only — it returned an empty line when only token caps
// were configured, so a token-only budget (the local-runner case where CostUSD
// is always 0) had no visible budget gauge. It must now render the binding
// token cap.
func TestSidebarBudgetLine_TokenOnly(t *testing.T) {
	t.Run("combined tokens", func(t *testing.T) {
		m := &Model{}
		m.budgetWarnTokens = 8000
		m.budgetHardTokens = 10000
		m.usage.InputTokens = 9000 // -> totalTokens 9000, past the warn cap
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

	// When both USD and token caps are configured, a breached token cap must
	// not be hidden behind an un-breached USD line (codex review on #137).
	t.Run("both caps surface the breached one", func(t *testing.T) {
		m := &Model{}
		m.budgetWarnUSD = 10
		m.budgetHardUSD = 20
		m.usage.CostUSD = 0 // USD not breached
		m.budgetHardTokens = 1000
		m.usage.InputTokens = 1500 // token hard cap breached
		line := m.sidebarBudgetLine()
		if !strings.Contains(line.Text, "tok") {
			t.Fatalf("breached token cap hidden behind un-breached USD line: %q", line.Text)
		}
		if line.Tone != "error" {
			t.Fatalf("breached hard token cap tone = %q, want error", line.Tone)
		}
	})

	// USD still wins the tie when neither (or both equally) breached, keeping
	// the common cloud-provider display unchanged.
	t.Run("both caps idle prefers USD", func(t *testing.T) {
		m := &Model{}
		m.budgetHardUSD = 20
		m.budgetHardTokens = 1000
		line := m.sidebarBudgetLine()
		if !strings.Contains(line.Text, "$") {
			t.Fatalf("idle both-caps should show the USD line, got %q", line.Text)
		}
	})
}
