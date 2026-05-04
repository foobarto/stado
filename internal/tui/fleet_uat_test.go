package tui

import (
	"strings"
	"testing"
)

// TestFleet_SlashSpawnRequiresPrompt: `/spawn` with no args produces
// a usage advisory rather than firing an empty agent.
func TestFleet_SlashSpawnRequiresPrompt(t *testing.T) {
	m := scenarioModel(t)
	_ = m.handleSlash("/spawn")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(strings.ToLower(last.body), "usage") {
		t.Errorf("expected usage advisory, got %+v", last)
	}
}

// TestFleet_SlashSpawnWithoutSession: /spawn with a real prompt but
// no parent session yet emits a "no active session" advisory rather
// than crashing. The Fleet's SubagentRunner builder requires a
// parent stadogit session — this test pins the guard.
func TestFleet_SlashSpawnWithoutSession(t *testing.T) {
	m := scenarioModel(t)
	// scenarioModel doesn't initialise m.session, mirroring a fresh
	// TUI that hasn't taken its first turn yet.
	_ = m.handleSlash("/spawn investigate this")
	found := false
	for _, b := range m.blocks {
		if b.kind == "system" && strings.Contains(strings.ToLower(b.body), "no active session") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'no active session' advisory; got blocks: %+v", m.blocks)
	}
}

// TestFleet_SlashFleetOpensModal: /fleet opens the picker modal,
// which renders the empty-state message when no agents have been
// spawned.
func TestFleet_SlashFleetOpensModal(t *testing.T) {
	m := scenarioModel(t)
	_ = m.handleSlash("/fleet")
	if m.fleetPicker == nil {
		t.Fatal("fleetPicker is nil — Model construction missed initialisation")
	}
	if !m.fleetPicker.Visible {
		t.Error("/fleet did not open the modal")
	}
	if len(m.fleetPicker.Items) != 0 {
		t.Errorf("expected empty modal on fresh fleet, got %d items", len(m.fleetPicker.Items))
	}
}
