package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func newBudgetModel(t *testing.T) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	// Minimum viable geometry so renderStatus / layout don't panic.
	m.width, m.height = 120, 40
	return m
}

// TestBudget_WarnPillAppearsOnlyAboveWarnCap: the status-bar pill
// renders when cumulative cost has crossed the warn cap and not
// before. This is the user-visible half of the budget guardrail.
func TestBudget_WarnPillAppearsOnlyAboveWarnCap(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudget(1.00, 5.00)

	if m.budgetWarning() != "" {
		t.Errorf("no cost accumulated yet; expected empty pill, got %q", m.budgetWarning())
	}
	m.usage.CostUSD = 0.75
	if m.budgetWarning() != "" {
		t.Errorf("cost below warn cap; expected empty pill, got %q", m.budgetWarning())
	}
	m.usage.CostUSD = 1.25
	pill := m.budgetWarning()
	if !strings.Contains(pill, "$1.25") || !strings.Contains(pill, "$5.00") {
		t.Errorf("expected pill to show $1.25 / $5.00; got %q", pill)
	}
}

// TestBudget_HardCapBlocksSubmit: once cumulative cost crosses the
// hard cap, pressing Enter surfaces a blocking system block and the
// pending turn is not started. /budget ack unblocks it for the rest
// of the session.
func TestBudget_HardCapBlocksSubmit(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudget(1.00, 2.00)
	m.usage.CostUSD = 2.50
	if !m.budgetExceeded() {
		t.Fatal("expected budgetExceeded=true")
	}

	// Simulate pressing Enter with a non-empty input.
	m.input.Reset()
	m.input.SetValue("try again")
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = m.Update(msg)

	if m.state == stateStreaming {
		t.Error("expected submit to be blocked; got streaming state")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected a system block warning about the cap")
	}
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "hard cap") {
		t.Errorf("block body missing 'hard cap' hint: %q", last)
	}

	// /budget ack unblocks: budgetExceeded flips to false regardless
	// of cost (user explicitly acknowledged).
	m.handleBudgetSlash([]string{"/budget", "ack"})
	if m.budgetExceeded() {
		t.Error("ack should clear the block")
	}
}

// TestBudget_WarnFiresOncePerSession: the one-time system block from
// maybeEmitBudgetWarning shouldn't repeat turn after turn.
func TestBudget_WarnFiresOncePerSession(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudget(1.00, 0)
	m.usage.CostUSD = 1.10

	startBlocks := len(m.blocks)
	m.maybeEmitBudgetWarning()
	m.maybeEmitBudgetWarning() // second call should be a no-op
	m.maybeEmitBudgetWarning() // third call too

	// Only one system block should have been appended.
	delta := len(m.blocks) - startBlocks
	if delta != 1 {
		t.Errorf("expected exactly 1 new block; got %d", delta)
	}
}

// TestBudget_NoCapNoPill: unset caps (default config) keep the pill
// empty and never block. Critical for local-runner users who don't
// care about cost and shouldn't see guardrail UI.
func TestBudget_NoCapNoPill(t *testing.T) {
	m := newBudgetModel(t)
	m.usage.CostUSD = 100.0

	if m.budgetWarning() != "" {
		t.Errorf("no cap configured; expected empty pill, got %q", m.budgetWarning())
	}
	if m.budgetExceeded() {
		t.Error("no cap configured; expected budgetExceeded=false")
	}
}
