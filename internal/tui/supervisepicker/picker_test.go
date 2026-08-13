package supervisepicker

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/supervise"
)

func TestWizardDefaultsToEventUserApprovalAndIndependentRoles(t *testing.T) {
	m := New()
	m.Open("ship it", "openai", "gpt-test")
	if m.Draft.Config.Mode != supervise.ModeEvent || m.Draft.Config.PivotApproval != supervise.PivotByUser {
		t.Fatalf("defaults = %+v", m.Draft.Config)
	}
	if m.Draft.Config.Watchdog.Provider != "openai" || m.Draft.Config.Verifier.Model != "gpt-test" {
		t.Fatalf("role defaults = %+v", m.Draft.Config)
	}
	// Move to the Start action and activate it.
	for i := 0; i < 5; i++ {
		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Visible || m.TakeResult().Action != ActionStart {
		t.Fatal("wizard did not produce start result")
	}
}

func TestWizardRequiresObjectiveAndExposesAdvancedReasoning(t *testing.T) {
	m := New()
	m.Open("", "", "")
	m.Cursor = 5
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Notice == "" || !m.Visible {
		t.Fatal("empty objective was accepted")
	}
	m.Advanced = true
	labels := map[string]bool{}
	for _, field := range m.fields() {
		labels[field.label] = true
	}
	for _, want := range []string{"Watchdog provider", "Watchdog thinking", "Watchdog thinking budget", "Watchdog effort", "Watchdog cost cap USD", "Verifier provider", "Verifier thinking", "Verifier thinking budget", "Verifier effort", "Verifier cost cap USD", "Live retry max milliseconds"} {
		if !labels[want] {
			t.Fatalf("advanced wizard missing %q", want)
		}
	}
}

func TestWizardPreservesFractionalCostCapWhileTyping(t *testing.T) {
	m := New()
	m.Open("ship it", "openai", "gpt-test")
	m.Advanced = true
	for i, f := range m.fields() {
		if f.label == "Watchdog cost cap USD" {
			m.Cursor = i
			break
		}
	}
	if m.fields()[m.Cursor].label != "Watchdog cost cap USD" {
		t.Fatal("watchdog cost cap field not found")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	for _, key := range []string{"0", ".", "5"} {
		_, _ = m.Update(tea.KeyPressMsg{Text: key})
	}
	if got := m.fields()[m.Cursor].get(); got != "0.5" {
		t.Fatalf("cost cap edit buffer = %q, want 0.5", got)
	}
	if got := m.Draft.Config.WatchdogBudget.CostCapUSD; got != 0.5 {
		t.Fatalf("cost cap = %v, want 0.5", got)
	}
	m.start()
	result := m.TakeResult()
	if result.Action != ActionStart || result.Draft.Config.WatchdogBudget.CostCapUSD != 0.5 {
		t.Fatalf("start result = %+v", result)
	}
}
