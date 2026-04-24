package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAgentPickerItemsMarkCurrent(t *testing.T) {
	items := agentPickerItems(modeBTW)
	var sawCurrent bool
	for _, it := range items {
		if it.ID == "btw" && it.Current {
			sawCurrent = true
		}
	}
	if !sawCurrent {
		t.Fatalf("current BTW agent not marked: %+v", items)
	}
}

func TestUAT_SlashAgentsOpensPicker(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	_ = m.handleSlash("/agents")
	if !m.agentPick.Visible {
		t.Fatal("/agents should open the agent picker")
	}
	if sel := m.agentPick.Selected(); sel == nil || sel.ID != "do" {
		t.Fatalf("default agent selection = %+v, want do", sel)
	}
}

func TestUAT_AgentPickerSubmitSetsMode(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	m.openAgentPicker()

	for _, r := range "btw" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.agentPick.Visible {
		t.Fatal("agent picker should close after selection")
	}
	if m.mode != modeBTW {
		t.Fatalf("mode = %s, want BTW", m.mode.String())
	}
}

func TestUAT_CtrlXAOpensAgentPicker(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if !m.agentPick.Visible {
		t.Fatal("ctrl+x a should open the agent picker")
	}
}
