package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/pkg/agent"
)

// textProvider streams a fixed assistant text then EvDone with no tool
// calls, so AgentLoop completes in a single turn.
type textProvider struct {
	text   string
	system string // captured from the request
}

func (p *textProvider) Name() string                     { return "text" }
func (p *textProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *textProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.system = req.System
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: p.text}
	ch <- agent.Event{Kind: agent.EvDone, Usage: &agent.Usage{InputTokens: 5, OutputTokens: 3}}
	close(ch)
	return ch, nil
}

// TestAgentLoop_PreLLMDeny_AbortsTurn: a pre_llm hook that denies aborts
// the loop with the reason surfaced as the error; the provider is never
// called.
func TestAgentLoop_PreLLMDeny_AbortsTurn(t *testing.T) {
	prov := &textProvider{text: "should not stream"}
	runner := hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "deny-llm",
		Subscribed: []hooks.Point{hooks.PointPreLLM},
		Fn: func(context.Context, hooks.Point, hooks.Payload) (hooks.HookResult, error) {
			return hooks.Deny("dry-run: no llm calls"), nil
		},
	})
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Hooks:    runner,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "dry-run: no llm calls") {
		t.Fatalf("expected pre_llm deny to abort with reason, got %v", err)
	}
	if prov.system != "" {
		t.Fatalf("provider was called despite a pre_llm deny (captured system=%q)", prov.system)
	}
}

// TestAgentLoop_PreLLMMutate_RewritesSystem: a pre_llm hook that mutates
// the system prompt changes what the provider receives.
func TestAgentLoop_PreLLMMutate_RewritesSystem(t *testing.T) {
	prov := &textProvider{text: "hi"}
	runner := hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "rewrite-system",
		Subscribed: []hooks.Point{hooks.PointPreLLM},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PreLLMPayload)
			clone.System = "REWRITTEN SYSTEM PROMPT"
			return hooks.Mutate(&clone), nil
		},
	})
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Hooks:    runner,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if prov.system != "REWRITTEN SYSTEM PROMPT" {
		t.Fatalf("pre_llm mutate did not rewrite the system prompt, got %q", prov.system)
	}
}

// TestAgentLoop_PostLLMMutate_RewritesText: a post_llm hook that mutates
// the assistant text changes the finalText AgentLoop returns.
func TestAgentLoop_PostLLMMutate_RewritesText(t *testing.T) {
	prov := &textProvider{text: "original answer"}
	runner := hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "tag",
		Subscribed: []hooks.Point{hooks.PointPostLLM},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PostLLMPayload)
			clone.Text = clone.Text + " [reviewed]"
			return hooks.Mutate(&clone), nil
		},
	})
	final, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Hooks:    runner,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if final != "original answer [reviewed]" {
		t.Fatalf("post_llm mutate did not rewrite the final text, got %q", final)
	}
}

// TestAgentLoop_PostTurnLifecycle_Fires: the post_turn lifecycle point
// fires on a completed turn boundary.
func TestAgentLoop_PostTurnLifecycle_Fires(t *testing.T) {
	prov := &textProvider{text: "done"}
	fired := false
	runner := hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "observe",
		Subscribed: []hooks.Point{hooks.PointPostTurn},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			if _, ok := p.(*hooks.PostTurnLifecyclePayload); ok {
				fired = true
			}
			return hooks.Continue(), nil
		},
	})
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Hooks:    runner,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if !fired {
		t.Fatalf("post_turn lifecycle hook did not fire")
	}
}

// TestAgentLoop_NilHooks_Unaffected: a nil Hooks runner is a no-op; the
// loop behaves exactly as before.
func TestAgentLoop_NilHooks_Unaffected(t *testing.T) {
	prov := &textProvider{text: "plain"}
	final, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if final != "plain" {
		t.Fatalf("nil hooks runner altered output: %q", final)
	}
}
