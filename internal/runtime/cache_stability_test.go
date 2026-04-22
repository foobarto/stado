package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// TestPrefixBytesStable asserts that the bytes that would be handed to a
// provider as the "stable cache prefix" — system + sorted tool defs +
// prior messages up through the cache breakpoint — are byte-identical
// across repeated construction. Fails loudly on any clock / UUID /
// map-iteration leak. DESIGN §"Prompt-cache awareness".
func TestPrefixBytesStable(t *testing.T) {
	// Fixed prior history: a user turn, an assistant turn with a tool_use,
	// and a tool_result. Realistic mid-session shape.
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "list files in pkg"}},
		}},
		{Role: agent.RoleAssistant, Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "I'll glob."}},
			{ToolUse: &agent.ToolUseBlock{ID: "t1", Name: "glob", Input: json.RawMessage(`{"pattern":"pkg/**"}`)}},
		}},
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "t1", Content: "pkg/agent/agent.go"}},
		}},
	}

	render := func(reg *tools.Registry) []byte {
		req := agent.TurnRequest{
			Model:    "test-model",
			System:   "You are a coding agent.",
			Messages: msgs,
			Tools:    ToolDefs(reg),
			CacheHints: []agent.CachePoint{
				{MessageIndex: len(msgs) - 1},
			},
		}
		// Serialise the full request — every field that flows into cache
		// identity. encoding/json sorts map keys so Schema() maps don't
		// leak iteration order.
		buf, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return buf
	}

	// 12 trials with tools registered in randomised order each time. All
	// must produce identical bytes.
	rnd := rand.New(rand.NewSource(7))
	names := []string{"read", "write", "edit", "glob", "grep", "bash"}

	var canonical []byte
	for trial := 0; trial < 12; trial++ {
		order := append([]string(nil), names...)
		rnd.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

		reg := tools.NewRegistry()
		for _, n := range order {
			reg.Register(fixtureTool{name: n, class: tool.ClassNonMutating})
		}
		got := render(reg)
		if trial == 0 {
			canonical = got
			continue
		}
		if string(canonical) != string(got) {
			t.Fatalf("trial %d: prefix bytes diverged\n  registration order: %v\n  first: %s\n  got:   %s",
				trial, order, canonical, got)
		}
	}
}

// TestAgentLoopGuardrailQuietOnCleanRun asserts the happy path: a loop
// with no in-place mutation of prior messages completes without triggering
// the append-only guardrail. Pairs with TestHashMessagesPrefixSensitive
// below (which asserts the helper detects mutation). The guardrail itself
// is only triggerable by code that runs between AgentLoop turns — which
// in a sequential program is only possible via ill-behaved callers across
// separate AgentLoop invocations, not inside one call.
func TestAgentLoopGuardrailQuietOnCleanRun(t *testing.T) {
	p := &scriptedProvider{
		capabilities: agent.Capabilities{SupportsPromptCache: false},
		turns: []scriptedTurn{
			{toolName: "stub", toolID: "call-1", toolInput: `{}`},
			{text: "done"},
		},
	}

	reg := tools.NewRegistry()
	reg.Register(fixtureTool{name: "stub", class: tool.ClassNonMutating})
	exec := &tools.Executor{Registry: reg}

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "hello"}}}},
	}
	if _, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: p,
		Executor: exec,
		Messages: msgs,
		MaxTurns: 5,
	}); err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
}

func TestAgentLoop_RejectsToolCallNotOfferedThisTurn(t *testing.T) {
	p := &scriptedProvider{
		turns: []scriptedTurn{
			{toolName: "bash", toolID: "call-1", toolInput: `{"command":"printf hi"}`},
			{text: "done"},
		},
	}

	finalText, finalMsgs, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: p,
		Executor: nil,
		Messages: []agent.Message{
			agent.Text(agent.RoleUser, "say hi"),
		},
		MaxTurns: 4,
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if finalText != "done" {
		t.Fatalf("finalText = %q, want %q", finalText, "done")
	}
	if len(finalMsgs) < 4 {
		t.Fatalf("finalMsgs = %d, want at least 4 messages", len(finalMsgs))
	}
	toolMsg := finalMsgs[2]
	if toolMsg.Role != agent.RoleTool || len(toolMsg.Content) != 1 || toolMsg.Content[0].ToolResult == nil {
		t.Fatalf("tool message malformed: %+v", toolMsg)
	}
	if !toolMsg.Content[0].ToolResult.IsError {
		t.Fatal("disallowed tool call should become an error tool result")
	}
	if got := toolMsg.Content[0].ToolResult.Content; got != `tool "bash" is not available for this turn` {
		t.Fatalf("tool result content = %q", got)
	}
}

// TestHashMessagesPrefixSensitive asserts that the guardrail's fingerprint
// function distinguishes structurally-different message prefixes — any
// byte-level change in any prior Block produces a different hash. This is
// the load-bearing property behind the AgentLoop mutation check.
func TestHashMessagesPrefixSensitive(t *testing.T) {
	base := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "hello"}}}},
		{Role: agent.RoleAssistant, Content: []agent.Block{{Text: &agent.TextBlock{Text: "hi"}}}},
	}
	h0 := hashMessagesPrefix(base, len(base))

	// Identical input → identical hash.
	if h1 := hashMessagesPrefix(base, len(base)); h1 != h0 {
		t.Fatalf("identical input produced different hashes: %s vs %s", h0, h1)
	}

	// Mutate msgs[0] in place — the scenario the guardrail is designed
	// to detect. Hash must change.
	mutated := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{{Text: &agent.TextBlock{Text: "TAMPERED"}}}},
		{Role: agent.RoleAssistant, Content: []agent.Block{{Text: &agent.TextBlock{Text: "hi"}}}},
	}
	if h2 := hashMessagesPrefix(mutated, len(mutated)); h2 == h0 {
		t.Fatalf("mutation produced same hash %s — guardrail would miss this", h0)
	}

	// Length-only change (append) → different hash from a longer snapshot,
	// but equal hash if we hash the prefix-only.
	appended := append(append([]agent.Message(nil), base...),
		agent.Message{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "x", Content: "ok"}},
		}})
	if h3 := hashMessagesPrefix(appended, len(base)); h3 != h0 {
		t.Fatalf("prefix hash changed after pure append: %s vs %s", h0, h3)
	}
}

// fixtureTool is a minimal tool.Tool for prefix-stability fixtures. Keeps
// this test file free of the tools package's heavier stubTool fixture.
type fixtureTool struct {
	name  string
	class tool.Class
}

func (f fixtureTool) Name() string          { return f.name }
func (f fixtureTool) Description() string   { return "fixture tool " + f.name }
func (f fixtureTool) Class() tool.Class     { return f.class }
func (f fixtureTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
		},
		"required": []any{"pattern"},
	}
}
func (fixtureTool) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

// scriptedProvider is a lightweight agent.Provider fake that replays a
// fixed sequence of turns over StreamTurn calls.
type scriptedProvider struct {
	capabilities agent.Capabilities
	turns        []scriptedTurn
	idx          int
}

type scriptedTurn struct {
	text      string
	toolName  string
	toolID    string
	toolInput string
}

func (p *scriptedProvider) Name() string                  { return "scripted" }
func (p *scriptedProvider) Capabilities() agent.Capabilities { return p.capabilities }

func (p *scriptedProvider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 4)
	if p.idx >= len(p.turns) {
		return nil, fmt.Errorf("scripted: no more turns (idx=%d)", p.idx)
	}
	t := p.turns[p.idx]
	p.idx++
	go func() {
		defer close(ch)
		if t.text != "" {
			ch <- agent.Event{Kind: agent.EvTextDelta, Text: t.text}
		}
		if t.toolName != "" {
			ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
				ID: t.toolID, Name: t.toolName, Input: json.RawMessage(t.toolInput),
			}}
		}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}
