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
	priorMsgs := len(m.msgs)

	_ = m.onTurnComplete()

	if m.steeringMsg != "" {
		t.Fatalf("steeringMsg should promote when the turn used no tools, got %q", m.steeringMsg)
	}
	// It must reach the LLM-facing history (promoted → fired), not just the
	// display blocks — a regression that clears steeringMsg without routing
	// through queuedPrompt would otherwise pass silently.
	if len(m.msgs) != priorMsgs+1 {
		t.Fatalf("promoted steer should add one msg to history: was %d, now %d", priorMsgs, len(m.msgs))
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

// When both a steer and a queued prompt are pending at a no-tool turn end,
// the queued prompt wins and the steer is dropped (with a notice) — never
// left to leak into a later turn.
func TestSteer_DroppedWhenQueuedAlsoSet(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.steeringMsg = "steer me"
	m.queuedPrompt = "queued instead"
	m.turnToolCalls = nil

	_ = m.onTurnComplete()

	if m.steeringMsg != "" {
		t.Fatalf("steeringMsg must be cleared, not leaked; got %q", m.steeringMsg)
	}
	if !hasSystemBlockContaining(m, "dropped") {
		t.Fatal("expected a 'steering dropped' notice when a queued prompt takes priority")
	}
}

// A cancelled turn drops its steer — it must not fire as the next turn.
func TestSteer_DroppedOnCancelledTurn(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.steeringMsg = "abandon me"
	m.turnCancelled = true
	m.turnToolCalls = nil
	priorMsgs := len(m.msgs)

	_ = m.onTurnComplete()

	if m.steeringMsg != "" {
		t.Fatalf("cancelled steer must be cleared, got %q", m.steeringMsg)
	}
	if m.queuedPrompt != "" {
		t.Fatalf("cancelled steer must not promote to a queued prompt, got %q", m.queuedPrompt)
	}
	if len(m.msgs) != priorMsgs {
		t.Fatalf("cancelled steer must not reach history: was %d, now %d", priorMsgs, len(m.msgs))
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

// /interrupt with no arg fires an already-queued prompt now (the old
// ForceQueue capability): cancel the turn, leave queuedPrompt for the
// cleanup to drain.
func TestInterrupt_NoArgFiresQueued(t *testing.T) {
	m := scenarioModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	m.queuedPrompt = "the queued one"

	_ = m.handleSlash("/interrupt")

	select {
	case <-ctx.Done():
	default:
		t.Fatal("/interrupt with a queued prompt should cancel the turn")
	}
	if m.queuedPrompt != "the queued one" {
		t.Fatalf("queued prompt should remain for the cancel cleanup to drain, got %q", m.queuedPrompt)
	}
}

func TestInterrupt_NoArgNothingQueued(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle

	_ = m.handleSlash("/interrupt")

	if !hasSystemBlockContaining(m, "nothing to run") {
		t.Fatal("/interrupt with nothing queued should report 'nothing to run'")
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
