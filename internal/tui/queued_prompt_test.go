package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
func TestQueuedPrompt_EnterWhileStreamingQueues(t *testing.T) {
	m := queueModel(t)
	// Simulate an in-flight turn.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	_ = ctx

	// Type "retry now" and hit Enter.
	for _, r := range "retry now" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.queuedPrompt != "retry now" {
		t.Errorf("queuedPrompt = %q, want %q", m.queuedPrompt, "retry now")
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after queue, got %q", m.input.Value())
	}
	if m.state != stateStreaming {
		t.Errorf("state = %v, should still be streaming after queue", m.state)
	}
	// Regression against dogfood bug: the queued message must appear
	// in m.blocks immediately so the user sees it in the chat, not
	// only after the current stream finishes.
	if len(m.blocks) == 0 {
		t.Fatal("queued message should appear in blocks immediately")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "user" || last.body != "retry now" {
		t.Errorf("last block = %+v, want user/'retry now'", last)
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

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})

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
