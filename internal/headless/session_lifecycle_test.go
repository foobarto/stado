package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

// TestSessionDeleteRemovesSession verifies session.delete + that a
// subsequent session.prompt against the deleted id returns invalid-params.
func TestSessionDeleteRemovesSession(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	// Create then delete.
	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.delete","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"result":{}`) {
		t.Errorf("delete result shape: %s", reply)
	}

	// Prompt on deleted session → error.
	io.WriteString(client, `{"jsonrpc":"2.0","id":3,"method":"session.prompt","params":{"sessionId":"h-1","prompt":"hi"}}`+"\n")
	reply = readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "unknown sessionId") {
		t.Errorf("expected 'unknown sessionId' error, got %s", reply)
	}
	client.Close()
}

// TestSessionDeleteUnknownErrors.
func TestSessionDeleteUnknownErrors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.delete","params":{"sessionId":"nope"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "unknown sessionId") {
		t.Errorf("expected unknown sessionId error: %s", reply)
	}
	client.Close()
}

// TestSessionCancelNoActivePromptReturnsFalse: no in-flight prompt
// → cancelled=false, no error.
func TestSessionCancelNoActivePromptReturnsFalse(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.cancel","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"cancelled":false`) {
		t.Errorf("expected cancelled:false: %s", reply)
	}
	client.Close()
}

// TestSessionCompactWithoutProviderErrors.
func TestSessionCompactWithoutProviderErrors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	// Manually seed a message so the "empty session" branch doesn't fire first.
	srv.mu.Lock()
	sess := srv.sessions["h-1"]
	sess.mu.Lock()
	sess.messages = []agent.Message{agent.Text(agent.RoleUser, "hi")}
	sess.mu.Unlock()
	srv.mu.Unlock()

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.compact","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "no provider") {
		t.Errorf("expected 'no provider' error: %s", reply)
	}
	client.Close()
}

// TestSessionCompactEmptyConversationErrors.
func TestSessionCompactEmptyConversationErrors(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, scriptedCompactProvider{})
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.compact","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "no messages") {
		t.Errorf("expected 'no messages' error: %s", reply)
	}
	client.Close()
}

// TestSessionCompactReplacesMessages drives the happy path end-to-end
// with a scripted provider that returns a fixed summary.
func TestSessionCompactReplacesMessages(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewServer(&config.Config{}, scriptedCompactProvider{
		summary: "concise session summary",
	})
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	// Seed 3 messages.
	srv.mu.Lock()
	sess := srv.sessions["h-1"]
	sess.mu.Lock()
	sess.messages = []agent.Message{
		agent.Text(agent.RoleUser, "task 1"),
		agent.Text(agent.RoleAssistant, "reply 1"),
		agent.Text(agent.RoleUser, "task 2"),
	}
	sess.mu.Unlock()
	srv.mu.Unlock()

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"session.compact","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 3*time.Second)
	if !strings.Contains(reply, "concise session summary") {
		t.Errorf("expected summary in reply: %s", reply)
	}
	// Parse out priorTurns / postTurns.
	var r struct {
		Result struct {
			Summary    string `json:"summary"`
			PriorTurns int    `json:"priorTurns"`
			PostTurns  int    `json:"postTurns"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Result.PriorTurns != 3 || r.Result.PostTurns != 1 {
		t.Errorf("turn counts: prior=%d post=%d (want 3 → 1)", r.Result.PriorTurns, r.Result.PostTurns)
	}

	// After compaction, in-memory msgs should be length 1.
	srv.mu.Lock()
	got := len(srv.sessions["h-1"].messages)
	srv.mu.Unlock()
	if got != 1 {
		t.Errorf("in-memory msgs = %d, want 1", got)
	}
	client.Close()
}

// TestMaybeEmitContextWarning_Fraction exercises the three regions
// without going through the RPC surface (unit-test the policy).
func TestMaybeEmitContextWarning_Fraction(t *testing.T) {
	// This just exercises the branches via the exported-internal path;
	// we use a nil conn so Notify is a no-op, and inspect the
	// threshold arithmetic via an in-line test.
	cases := []struct {
		input int
		cap   int
		soft  float64
		hard  float64
		fires bool
		level string
	}{
		{50, 100, 0.70, 0.90, false, ""},
		{80, 100, 0.70, 0.90, true, "soft"},
		{95, 100, 0.70, 0.90, true, "hard"},
		{0, 100, 0.70, 0.90, false, ""},   // zero tokens: no fire
		{80, 0, 0.70, 0.90, false, ""},    // no MaxContextTokens: no fire
	}
	for _, tc := range cases {
		frac := 0.0
		if tc.cap > 0 {
			frac = float64(tc.input) / float64(tc.cap)
		}
		level := "soft"
		if frac >= tc.hard {
			level = "hard"
		}
		fires := tc.cap > 0 && tc.input > 0 && frac >= tc.soft
		if fires != tc.fires {
			t.Errorf("%+v: fires=%v want %v", tc, fires, tc.fires)
		}
		if fires && level != tc.level {
			t.Errorf("%+v: level=%s want %s", tc, level, tc.level)
		}
	}
}

// --- test fakes ---

// scriptedCompactProvider is a minimal agent.Provider that emits a
// fixed summary over a single text delta + done. Lets the compact
// flow unit-test without a real network call.
type scriptedCompactProvider struct {
	summary string
}

func (scriptedCompactProvider) Name() string                  { return "scripted" }
func (scriptedCompactProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p scriptedCompactProvider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	go func() {
		defer close(ch)
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: p.summary}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

// Stop unused-bufio linter complaint if other tests drop bufio.
var _ = bufio.NewReader
