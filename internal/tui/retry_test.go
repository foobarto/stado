package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// retryStub is a minimal non-nil provider so startStream's
// `m.provider.Capabilities()` call doesn't nil-deref. We don't care
// about the streamed response here — retry tests only assert the
// m.msgs / m.blocks truncation, which happens before the goroutine
// actually pulls events.
type retryStub struct{}

func (retryStub) Name() string                     { return "retry" }
func (retryStub) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (retryStub) StreamTurn(ctx context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, nil
}

func newRetryModel(t *testing.T) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return retryStub{}, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 40
	return m
}

// TestRetry_NoUserMessageYetIsHandled: /retry on a fresh session
// with no user messages explains itself instead of crashing or
// silently no-opping.
func TestRetry_NoUserMessageYetIsHandled(t *testing.T) {
	m := newRetryModel(t)
	_ = m.handleRetrySlash()
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "nothing to retry") {
		t.Errorf("expected 'nothing to retry' hint; got %q", last)
	}
}

// TestRetry_TruncatesPastLastAssistant: when the conversation ends
// in an assistant message, /retry pops it back to the last user
// message. m.msgs shortens accordingly so the next stream regenerates
// from the same prompt. The visible block list also shrinks back to
// the user block so the screen matches.
func TestRetry_TruncatesPastLastAssistant(t *testing.T) {
	m := newRetryModel(t)
	// Seed conversation: user -> assistant.
	m.msgs = []agent.Message{
		agent.Text(agent.RoleUser, "first"),
		agent.Text(agent.RoleAssistant, "reply"),
	}
	m.blocks = []block{
		{kind: "user", body: "first"},
		{kind: "assistant", body: "reply"},
	}

	_ = m.handleRetrySlash()

	if len(m.msgs) != 1 || m.msgs[0].Role != agent.RoleUser {
		t.Fatalf("expected msgs to truncate to [user]; got %+v", m.msgs)
	}
	// Expected blocks: user + system 'regenerating...' (startStream
	// may return nil here since ensureProvider fails on our stub,
	// but the truncation must still have happened).
	foundUser := false
	for _, b := range m.blocks {
		if b.kind == "user" && b.body == "first" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Error("user block lost after retry")
	}
}

// TestRetry_LastMessageIsUserTellsUserToJustSubmit: if the
// conversation already ends in a user prompt (no assistant yet),
// /retry can't regenerate anything; it hints the user to submit.
func TestRetry_LastMessageIsUserTellsUserToJustSubmit(t *testing.T) {
	m := newRetryModel(t)
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "waiting")}
	_ = m.handleRetrySlash()
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "already a user prompt") {
		t.Errorf("expected submit hint; got %q", last)
	}
}

// TestRetry_RefusesDuringStream: /retry during an active stream is
// a no-op + warning — re-running while the first turn is still
// producing events would double the costs and race the goroutine.
func TestRetry_RefusesDuringStream(t *testing.T) {
	m := newRetryModel(t)
	m.state = stateStreaming
	m.msgs = []agent.Message{
		agent.Text(agent.RoleUser, "x"),
		agent.Text(agent.RoleAssistant, "y"),
	}
	_ = m.handleRetrySlash()
	last := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(last, "wait for the current turn") {
		t.Errorf("expected busy-state hint; got %q", last)
	}
	// Must NOT have truncated.
	if len(m.msgs) != 2 {
		t.Errorf("msgs unexpectedly mutated during stream: %+v", m.msgs)
	}
}
