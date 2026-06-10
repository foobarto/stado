package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// #16 — steering: Enter/​/steer inject mid-turn; alt+enter queues;
// ctrl+enter interrupts. See
// .agent/decisions/2026-06-10-steering-queue-interrupt-model.md.

func TestSteer_SlashSetsWhileStreaming(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	_ = m.handleSlash("/steer turn left")
	if m.steeringMsg != "turn left" {
		t.Fatalf("steeringMsg = %q, want %q", m.steeringMsg, "turn left")
	}
}

// At a tool boundary the buffered steering message is appended to the
// live conversation (m.msgs) so the model sees it on the next round-trip.
func TestSteer_DrainAtToolBoundary(t *testing.T) {
	m := scenarioModel(t)
	m.steeringMsg = "steer me"
	before := len(m.msgs)

	m.drainSteering()

	if m.steeringMsg != "" {
		t.Fatalf("steeringMsg should clear after drain, got %q", m.steeringMsg)
	}
	if len(m.msgs) != before+1 {
		t.Fatalf("drain should add one msg: was %d, now %d", before, len(m.msgs))
	}
	found := false
	for _, b := range m.blocks {
		if b.kind == "user" && b.body == "steer me" {
			found = true
		}
	}
	if !found {
		t.Fatal("injected steering user block not found")
	}
}

// A turn that used no tools has no boundary to ride, so steering promotes
// to the next turn at completion.
func TestSteer_PromotedWhenNoTools(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.steeringMsg = "follow up"
	m.turnToolCalls = nil

	_ = m.onTurnComplete()

	if m.steeringMsg != "" {
		t.Fatalf("steeringMsg should promote when the turn used no tools, got %q", m.steeringMsg)
	}
	found := false
	for _, b := range m.blocks {
		if b.kind == "user" && b.body == "follow up" {
			found = true
		}
	}
	if !found {
		t.Fatal("promoted steering message 'follow up' not found in blocks")
	}
}

func TestSteer_AltEnterQueues(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.input.SetValue("later please")

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})

	if m.queuedPrompt != "later please" {
		t.Fatalf("alt+enter should queue, queuedPrompt = %q", m.queuedPrompt)
	}
	if m.steeringMsg != "" {
		t.Fatalf("alt+enter should not steer, steeringMsg = %q", m.steeringMsg)
	}
}

func TestInterrupt_CtrlEnterCancelsAndRuns(t *testing.T) {
	m := scenarioModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	m.input.SetValue("do this now")

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})

	if m.queuedPrompt != "do this now" {
		t.Fatalf("ctrl+enter should queue the message to run after cancel, got %q", m.queuedPrompt)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("ctrl+enter should have cancelled the in-flight stream")
	}
}

func TestSteer_EscClears(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.steeringMsg = "nevermind"

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.steeringMsg != "" {
		t.Fatalf("Esc should clear steeringMsg, got %q", m.steeringMsg)
	}
}
