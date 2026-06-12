package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
	msg := tea.KeyPressMsg{Code: tea.KeyEnter}
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

// TestBudget_TokenHardCapBlockMessageIsCoherent: when a turn is
// blocked by a TOKEN hard cap (no USD cap set), the blocking system
// block must name the token cap that fired — not claim "cost $0.00 ≥
// hard cap $0.00" and point the user at [budget].hard_usd, which is
// unset and irrelevant. Regression guard for the recovery-flow drift
// where the gate only spoke USD.
func TestBudget_TokenHardCapBlockMessageIsCoherent(t *testing.T) {
	m := newBudgetModel(t)
	// Token-only budget: combined hard cap at 1000 tokens, no USD cap.
	m.SetBudgetTokens(500, 1000)
	m.usage.InputTokens = 800
	m.usage.OutputTokens = 400 // total 1200 ≥ 1000
	if !m.budgetExceeded() {
		t.Fatal("expected budgetExceeded=true on token cap")
	}

	m.input.Reset()
	m.input.SetValue("keep going")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.state == stateStreaming {
		t.Error("expected submit to be blocked; got streaming state")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected a system block warning about the cap")
	}
	body := m.blocks[len(m.blocks)-1].body

	// The bug: USD-only message on a token breach.
	if strings.Contains(body, "$0.00") {
		t.Errorf("token breach reported a $0.00 USD cap: %q", body)
	}
	if strings.Contains(body, "hard_usd") {
		t.Errorf("token breach pointed user at irrelevant [budget].hard_usd: %q", body)
	}
	// It must name the actual cap that fired and the right knob.
	if !strings.Contains(body, "1,000") && !strings.Contains(body, "1000") && !strings.Contains(body, "1.0k") {
		t.Errorf("block body should name the 1000-token hard cap: %q", body)
	}
	if !strings.Contains(body, "/budget ack") {
		t.Errorf("block body should still offer /budget ack: %q", body)
	}
}

// TestBudget_DisplayShowsTokenCaps: `/budget` (the display form) must
// surface configured token caps, not just USD. A token-only budget
// user who runs /budget should not see everything reported "(unset)".
func TestBudget_DisplayShowsTokenCaps(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudgetTokens(500, 1000)
	m.usage.InputTokens = 300
	m.usage.OutputTokens = 100

	start := len(m.blocks)
	m.handleBudgetSlash([]string{"/budget"})
	if len(m.blocks) == start {
		t.Fatal("expected /budget to append a system block")
	}
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "1,000") && !strings.Contains(body, "1000") && !strings.Contains(body, "1.0k") {
		t.Errorf("/budget display omitted the 1000-token hard cap: %q", body)
	}
}

// TestBudget_TokenOnlyWarnFiresProactiveBlock: a token-only budget
// (warn/hard tokens set, USD unset — the local-runner case where
// CostUSD is always 0) must get the one-time proactive warn block
// before the hard cap blocks the turn. Regression guard: the warn
// gate used to bail when budgetWarnUSD <= 0, so a token-only user got
// no advisory at all and was surprised by the hard block.
func TestBudget_TokenOnlyWarnFiresProactiveBlock(t *testing.T) {
	m := newBudgetModel(t)
	// Token-only: warn at 500, hard at 1000, no USD cap.
	m.SetBudgetTokens(500, 1000)
	m.usage.InputTokens = 400
	m.usage.OutputTokens = 200 // total 600 ≥ 500 warn, < 1000 hard

	start := len(m.blocks)
	m.maybeEmitBudgetWarning()
	if len(m.blocks) == start {
		t.Fatal("token-only budget crossed the warn cap but no warn block was emitted")
	}
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "600") && !strings.Contains(body, "tok") {
		t.Errorf("warn block should report the 600-token usage: %q", body)
	}
	if !strings.Contains(body, "500") {
		t.Errorf("warn block should name the 500-token warn cap: %q", body)
	}
	if strings.Contains(body, "$") {
		t.Errorf("token-only warn block should not speak USD: %q", body)
	}
	// One-time: subsequent calls are no-ops.
	again := len(m.blocks)
	m.maybeEmitBudgetWarning()
	if len(m.blocks) != again {
		t.Error("token-only warn block should fire once per session")
	}
}

// TestBudget_ContextStatusShowsTokenCaps: the /context-style budget
// line in renderContextStatus must surface configured token caps. A
// token-only budget user should see their token usage and caps, not a
// budget line that's omitted entirely (USD-only path).
func TestBudget_ContextStatusShowsTokenCaps(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudgetTokens(500, 1000)
	m.usage.InputTokens = 300
	m.usage.OutputTokens = 100 // total 400

	out := m.renderContextStatus()
	if !strings.Contains(out, "1,000") && !strings.Contains(out, "1000") && !strings.Contains(out, "1.0k") {
		t.Errorf("context status omitted the 1000-token hard cap: %q", out)
	}
	if !strings.Contains(out, "500") {
		t.Errorf("context status omitted the 500-token warn cap: %q", out)
	}
}

// TestBudget_StatusModalShowsTokenCaps: the /status modal budget row
// must render token caps for a token-only budget instead of showing
// "warn $0.00, hard $0.00" (or omitting the cap entirely).
func TestBudget_StatusModalShowsTokenCaps(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudgetTokens(500, 1000)
	m.usage.InputTokens = 300
	m.usage.OutputTokens = 100

	rows := m.statusContextRows()
	var budgetVal string
	for _, r := range rows {
		if r.Key == "budget" {
			budgetVal = r.Value
		}
	}
	if budgetVal == "" {
		t.Fatal("status modal has no budget row")
	}
	if strings.Contains(budgetVal, "$0.00") {
		t.Errorf("token-only budget row reported a $0.00 USD cap: %q", budgetVal)
	}
	if !strings.Contains(budgetVal, "1,000") && !strings.Contains(budgetVal, "1000") && !strings.Contains(budgetVal, "1.0k") {
		t.Errorf("status modal budget row omitted the 1000-token hard cap: %q", budgetVal)
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
