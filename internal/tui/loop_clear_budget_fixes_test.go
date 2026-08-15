package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/modelpicker"
)

// TestLoop_StopsOnBudgetHardCap: a /loop iteration must respect the same
// token-budget hard-cap gate a manual Enter does. The safe behavior is to STOP
// the loop (no human is
// present to /budget ack) and say why — not silently spin.
func TestLoop_StopsOnBudgetHardCap(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudgetTokens(100, 200)
	m.cumulativeInputTokens = 250 // over the hard cap
	if !m.budgetExceeded() {
		t.Fatal("precondition: expected budgetExceeded=true")
	}
	m.loop = &loopState{prompt: "keep going"}
	m.state = stateIdle

	cmd := m.loopIterate()

	if m.loop != nil {
		t.Error("loop should be stopped when the budget hard cap is hit")
	}
	if m.state == stateStreaming {
		t.Error("loop iteration should not start a stream over budget")
	}
	if cmd != nil {
		t.Error("over-budget loop iteration should not return a stream command")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected a system block explaining the loop stopped")
	}
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "loop stopped") {
		t.Errorf("block should say the loop stopped: %q", body)
	}
}

// TestLoop_StopsOnContextHardThreshold: the same gate for the context
// hard-threshold — a loop must not start a fresh turn once context is at/over
// the hard bound (a manual Enter is blocked there too).
func TestLoop_StopsOnContextHardThreshold(t *testing.T) {
	m := newBudgetModel(t)
	m.provider = fakeCappedProvider{max: 100}
	m.SetContextThresholds(0.70, 0.90)
	m.usage.InputTokens = 95 // 95/100 ≥ 0.90 hard threshold
	if !m.aboveHardThreshold() {
		t.Fatal("precondition: expected aboveHardThreshold=true")
	}
	m.loop = &loopState{prompt: "keep going"}
	m.state = stateIdle

	cmd := m.loopIterate()

	if m.loop != nil {
		t.Error("loop should be stopped above the context hard threshold")
	}
	if m.state == stateStreaming {
		t.Error("loop iteration should not start a stream above the hard threshold")
	}
	if cmd != nil {
		t.Error("over-threshold loop iteration should not return a stream command")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected a system block explaining the loop stopped")
	}
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "loop stopped") {
		t.Errorf("block should say the loop stopped: %q", body)
	}
}

// TestModel_WarnsOnUnknownModel: `/model <id>` with an id absent from the
// active provider's static catalog should warn (typo guard) while still
// setting the model (the catalog isn't exhaustive; pre-1.0 keeps the
// override). A catalogued id must NOT warn.
func TestModel_WarnsOnUnknownModel(t *testing.T) {
	cat := modelpicker.CatalogFor("anthropic")
	if len(cat) == 0 {
		t.Skip("no static catalog for anthropic — nothing to validate against")
	}
	knownID := cat[0].ID

	t.Run("unknown id warns but still sets", func(t *testing.T) {
		m := newBudgetModel(t)
		m.providerName = "anthropic"
		m.handleSlash("/model claude-totally-bogus-typo")
		if m.model != "claude-totally-bogus-typo" {
			t.Errorf("model should still be set to the requested id, got %q", m.model)
		}
		if len(m.blocks) == 0 {
			t.Fatal("expected a system block")
		}
		body := m.blocks[len(m.blocks)-1].body
		if !strings.Contains(body, "not in the known catalog") {
			t.Errorf("expected an unknown-model warning, got %q", body)
		}
	})

	t.Run("known id does not warn", func(t *testing.T) {
		m := newBudgetModel(t)
		m.providerName = "anthropic"
		m.handleSlash("/model " + knownID)
		if m.model != knownID {
			t.Errorf("model should be set to %q, got %q", knownID, m.model)
		}
		for _, b := range m.blocks {
			if strings.Contains(b.body, "not in the known catalog") {
				t.Errorf("catalogued id %q should not warn: %q", knownID, b.body)
			}
		}
	})

	// The false-positive guard: a provider with NO static catalog (local
	// runners, presets, OAI-compat custom) can't be validated, so /model must
	// NOT warn — otherwise every legitimate local/preset model id is flagged.
	t.Run("no warning when provider has no catalog", func(t *testing.T) {
		const noCatProvider = "totally-uncatalogued-provider"
		if len(modelpicker.CatalogFor(noCatProvider)) != 0 {
			t.Skipf("%q unexpectedly has a static catalog", noCatProvider)
		}
		m := newBudgetModel(t)
		m.providerName = noCatProvider
		m.handleSlash("/model my-local-model-7b")
		if m.model != "my-local-model-7b" {
			t.Errorf("model should be set, got %q", m.model)
		}
		for _, b := range m.blocks {
			if strings.Contains(b.body, "not in the known catalog") {
				t.Errorf("uncatalogued provider should not warn: %q", b.body)
			}
		}
	})
}

// TestClear_StopsLoopAndMonitor: /clear wipes the conversation, so it must
// also halt the background /loop and /monitor that were driving it —
// otherwise the loop ↻ indicator points at a wiped context and the monitor
// goroutine streams into a cleared screen (orphaned, per the UAT finding).
func TestClear_StopsLoopAndMonitor(t *testing.T) {
	m := newBudgetModel(t)
	m.loop = &loopState{prompt: "keep going"}
	canceled := false
	m.monitor = &monitorState{cmd: "tail -f log", cancel: func() { canceled = true }, gen: 1}

	m.handleSlash("/clear")

	if m.loop != nil {
		t.Error("/clear should stop the active loop")
	}
	if m.monitor != nil {
		t.Error("/clear should stop the active monitor")
	}
	if !canceled {
		t.Error("/clear should cancel the monitor's context (orphan goroutine otherwise)")
	}
}
