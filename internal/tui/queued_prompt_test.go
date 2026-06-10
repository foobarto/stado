package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// queueModel is a shared harness: Model with a mock streaming state
// and a working key registry. No provider — the tests drive state
// transitions directly instead of booting a real turn.
func queueModel(t *testing.T) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.width, m.height = 120, 30
	return m
}

// TestQueuedPrompt_EnterWhileStreamingQueues: typing a prompt + Enter
// while state=stateStreaming must queue — not drop silently (old
// behaviour) and not abruptly cancel the stream.
// #16: Enter while a turn is streaming now STEERS (buffers for mid-turn
// injection) rather than queuing for the next turn. The queue path moved
// to alt+enter / /queue (see steering_test.go, queue_test.go).
func TestQueuedPrompt_EnterWhileStreamingSteers(t *testing.T) {
	m := queueModel(t)
	// Simulate an in-flight turn.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	_ = ctx

	// Type "go left instead" and hit Enter.
	for _, r := range "go left instead" {
		_, _ = m.Update(tea.KeyPressMsg{Text: string(r)})
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.steeringMsg != "go left instead" {
		t.Errorf("steeringMsg = %q, want %q", m.steeringMsg, "go left instead")
	}
	if m.queuedPrompt != "" {
		t.Errorf("Enter while busy should steer, not queue; queuedPrompt = %q", m.queuedPrompt)
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after steer, got %q", m.input.Value())
	}
	if m.state != stateStreaming {
		t.Errorf("state = %v, should still be streaming after steer", m.state)
	}
	// The steering note appears immediately so the user sees it landed.
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "btw" || !contains(last.body, "steering") {
		t.Errorf("last block = %+v, want a btw steering note", last)
	}
}

// TestQueuedPrompt_EscClearsQueueFirst: when a queued prompt exists,
// Esc / Ctrl+G clears the queue — it does NOT also cancel the
// running stream. Those are two user intents; we handle one per
// press. (Ctrl+C now only clears chat input per the v0.28.0
// keybinding cleanup.)
func TestQueuedPrompt_EscClearsQueueFirst(t *testing.T) {
	m := queueModel(t)
	cancelled := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = func() { cancelled = true; cancel() }
	_ = ctx
	m.queuedPrompt = "buffered thing"

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.queuedPrompt != "" {
		t.Errorf("queuedPrompt = %q, want cleared", m.queuedPrompt)
	}
	if cancelled {
		t.Error("stream cancel should not fire on first Esc while queued — take two presses")
	}
}

// TestQueuedPrompt_StatusRowShowsQueuedExcerpt — the rendered status
// strip must mention the queued prompt so the user sees it's buffered.
func TestQueuedPrompt_StatusRowShowsQueuedExcerpt(t *testing.T) {
	m := queueModel(t)
	m.queuedPrompt = "double-check the migration script"

	got := m.renderStatus(120)
	if !strings.Contains(got, "queued:") {
		t.Errorf("status row missing 'queued:' label: %q", got)
	}
	if !strings.Contains(got, "double-check the migration") {
		t.Errorf("status row should include queued excerpt: %q", got)
	}
}

// TestQueuedPrompt_EmptyQueueNoPill — when nothing's queued the pill
// must not render (avoids empty "queued:" rendering noise).
func TestQueuedPrompt_EmptyQueueNoPill(t *testing.T) {
	m := queueModel(t)
	got := m.renderStatus(120)
	if strings.Contains(got, "queued:") {
		t.Errorf("empty queue should not render pill: %q", got)
	}
}
