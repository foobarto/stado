package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestBridge_ReadField_MessageCount — the simplest field. Bridge
// returns decimal-ASCII count of MessagesFn's current slice.
func TestBridge_ReadField_MessageCount(t *testing.T) {
	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "hi"),
		agent.Text(agent.RoleAssistant, "hey"),
		agent.Text(agent.RoleUser, "can you"),
	}
	b := &SessionBridgeImpl{MessagesFn: func() []agent.Message { return msgs }}
	data, err := b.ReadField("message_count")
	if err != nil {
		t.Fatalf("ReadField: %v", err)
	}
	if string(data) != "3" {
		t.Errorf("message_count = %q, want %q", data, "3")
	}
}

// TestBridge_ReadField_TokenCount_NoFnReturnsZero — absence of a
// TokensFn shouldn't surface as an error; "0" is the defensible
// value for "we don't have usage reported yet."
func TestBridge_ReadField_TokenCount_NoFnReturnsZero(t *testing.T) {
	b := &SessionBridgeImpl{}
	data, err := b.ReadField("token_count")
	if err != nil {
		t.Fatalf("ReadField: %v", err)
	}
	if string(data) != "0" {
		t.Errorf("token_count = %q, want 0", data)
	}
}

// TestBridge_ReadField_TokenCount_WithFn — when TokensFn is wired,
// its return value is stringified verbatim.
func TestBridge_ReadField_TokenCount_WithFn(t *testing.T) {
	b := &SessionBridgeImpl{TokensFn: func() int { return 42000 }}
	data, _ := b.ReadField("token_count")
	if string(data) != "42000" {
		t.Errorf("token_count = %q, want 42000", data)
	}
}

// TestBridge_ReadField_History_FlattensBlocks — the canonical
// multimodal message content (text + tool_use + tool_result + image
// + thinking) should collapse into a single string per message with
// placeholder tags. Plugins that want richer access will use
// field-specific imports later; `history` is the common case.
func TestBridge_ReadField_History_FlattensBlocks(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "look at"}},
			{Image: &agent.ImageBlock{}},
		}},
		{Role: agent.RoleAssistant, Content: []agent.Block{
			{Thinking: &agent.ThinkingBlock{}},
			{Text: &agent.TextBlock{Text: "I see a chart."}},
			{ToolUse: &agent.ToolUseBlock{Name: "read"}},
		}},
	}
	b := &SessionBridgeImpl{MessagesFn: func() []agent.Message { return msgs }}
	data, err := b.ReadField("history")
	if err != nil {
		t.Fatalf("ReadField: %v", err)
	}
	var arr []struct {
		Role, Text string
	}
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(arr), arr)
	}
	if arr[0].Role != "user" || !strings.Contains(arr[0].Text, "look at") || !strings.Contains(arr[0].Text, "[image]") {
		t.Errorf("user entry: %+v", arr[0])
	}
	if arr[1].Role != "assistant" ||
		!strings.Contains(arr[1].Text, "[thinking]") ||
		!strings.Contains(arr[1].Text, "I see a chart.") ||
		!strings.Contains(arr[1].Text, "[tool_use read]") {
		t.Errorf("assistant entry: %+v", arr[1])
	}
}

// TestBridge_ReadField_UnknownRejects — an unknown field name is a
// deterministic error, not silent-empty. Plugins that typo field
// names should see the mistake.
func TestBridge_ReadField_UnknownRejects(t *testing.T) {
	b := &SessionBridgeImpl{}
	_, err := b.ReadField("not_a_real_field")
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error text should mention unknown field: %v", err)
	}
}

// TestBridge_NextEvent_EmptyReturnsYieldSignal — the polling contract:
// no event available → return ([]byte{}, nil), which host imports
// translate to "0 bytes written" and the plugin yields. A nil return
// with a nil error is the yield-signal, not a bug.
func TestBridge_NextEvent_EmptyReturnsYieldSignal(t *testing.T) {
	b := &SessionBridgeImpl{Events: make(chan []byte, 4)}
	ev, err := b.NextEvent(context.Background())
	if err != nil {
		t.Fatalf("NextEvent: %v", err)
	}
	if len(ev) != 0 {
		t.Errorf("expected empty event, got %q", ev)
	}
}

// TestBridge_NextEvent_ReturnsQueued — after Emit, NextEvent pops
// that payload. Subsequent call yields again.
func TestBridge_NextEvent_ReturnsQueued(t *testing.T) {
	b := &SessionBridgeImpl{Events: make(chan []byte, 4)}
	b.Emit([]byte(`{"kind":"turn_complete","turn":3}`))
	ev, err := b.NextEvent(context.Background())
	if err != nil {
		t.Fatalf("NextEvent: %v", err)
	}
	if !strings.Contains(string(ev), "turn_complete") {
		t.Errorf("unexpected event: %q", ev)
	}
	ev2, _ := b.NextEvent(context.Background())
	if len(ev2) != 0 {
		t.Errorf("expected empty after pop, got %q", ev2)
	}
}

// TestBridge_Emit_DoesNotBlockWhenBufferFull — Emit must be
// non-blocking so a stalled plugin can't back-pressure the host.
// We fill the buffer and assert the extra Emit doesn't deadlock.
func TestBridge_Emit_DoesNotBlockWhenBufferFull(t *testing.T) {
	b := &SessionBridgeImpl{Events: make(chan []byte, 2)}
	b.Emit([]byte("a"))
	b.Emit([]byte("b"))
	b.Emit([]byte("c-dropped")) // buffer full; must not block
	// Pop the two that made it.
	ev, _ := b.NextEvent(context.Background())
	if string(ev) != "a" {
		t.Errorf("first event = %q, want a", ev)
	}
}

// TestBridge_Fork_RequiresForkFn — without a ForkFn the bridge must
// report it clearly; silently succeeding with an empty ID would let
// an auto-compaction plugin think it forked when it didn't.
func TestBridge_Fork_RequiresForkFn(t *testing.T) {
	b := &SessionBridgeImpl{}
	_, err := b.Fork(context.Background(), "turns/5", "summary")
	if err == nil {
		t.Fatal("expected error without ForkFn")
	}
}

// TestBridge_InvokeLLM_AggregatesDeltas — a stream of EvTextDelta
// events concatenates into the reply; EvUsage reports tokens.
func TestBridge_InvokeLLM_AggregatesDeltas(t *testing.T) {
	p := fakeStreamProvider{
		events: []agent.Event{
			{Kind: agent.EvTextDelta, Text: "Hello "},
			{Kind: agent.EvTextDelta, Text: "world."},
			{Kind: agent.EvUsage, Usage: &agent.Usage{InputTokens: 10, OutputTokens: 3}},
			{Kind: agent.EvDone},
		},
	}
	b := &SessionBridgeImpl{Provider: p, Model: "fake"}
	reply, tokens, err := b.InvokeLLM(context.Background(), "hi", LLMInvokeOpts{})
	if err != nil {
		t.Fatalf("InvokeLLM: %v", err)
	}
	if reply != "Hello world." {
		t.Errorf("reply = %q", reply)
	}
	if tokens != 13 {
		t.Errorf("tokens = %d, want 13", tokens)
	}
}

// TestBridge_InvokeLLM_FallbackTokenEstimate — when the provider
// emits no EvUsage event, the bridge falls back to a byte-heuristic
// so budget enforcement still has teeth.
func TestBridge_InvokeLLM_FallbackTokenEstimate(t *testing.T) {
	p := fakeStreamProvider{
		events: []agent.Event{
			{Kind: agent.EvTextDelta, Text: "reply text here"},
			{Kind: agent.EvDone},
		},
	}
	b := &SessionBridgeImpl{Provider: p, Model: "fake"}
	_, tokens, err := b.InvokeLLM(context.Background(), "prompt", LLMInvokeOpts{})
	if err != nil {
		t.Fatalf("InvokeLLM: %v", err)
	}
	if tokens <= 0 {
		t.Errorf("fallback estimate should be positive, got %d", tokens)
	}
}

// TestBridge_InvokeLLM_ProviderError — provider-side errors surface;
// an error here blocks the budget counter from incrementing so a
// degenerate provider can't drain the quota.
func TestBridge_InvokeLLM_ProviderError(t *testing.T) {
	p := fakeStreamProvider{err: errors.New("provider boom")}
	b := &SessionBridgeImpl{Provider: p, Model: "fake"}
	_, _, err := b.InvokeLLM(context.Background(), "x", LLMInvokeOpts{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// fakeStreamProvider is a minimal agent.Provider for bridge tests.
// Sends the pre-seeded events over the channel and closes.
type fakeStreamProvider struct {
	events []agent.Event
	err    error
}

func (fakeStreamProvider) Name() string                      { return "fake" }
func (fakeStreamProvider) Capabilities() agent.Capabilities  { return agent.Capabilities{} }
func (p fakeStreamProvider) StreamTurn(ctx context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan agent.Event, len(p.events))
	for _, ev := range p.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}
