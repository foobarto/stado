package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// The interactive TUI streams the provider call directly (not through
// runtime.AgentLoop), so the pre_llm / post_llm lifecycle points must be
// wired into the TUI stream loop separately. These tests exercise those
// two seams with builtin (Go-closure) hooks, mirroring the agentloop
// semantics: pre_llm can deny (abort the turn) or mutate (system+model);
// post_llm can deny (replace the assistant text) or mutate (rewrite it).

func newLifecycleTestModel(t *testing.T, prov agent.Provider) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return prov, nil }, rnd, keys.NewRegistry())
	// renderBlocks needs non-zero dimensions.
	m.width, m.height = 80, 24
	return m
}

// preLLMDenyHook denies every pre_llm with the given reason.
func preLLMDenyHook(reason string) *hooks.LifecycleRunner {
	return hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "deny-pre-llm",
		Subscribed: []hooks.Point{hooks.PointPreLLM},
		Fn: func(_ context.Context, _ hooks.Point, _ hooks.Payload) (hooks.HookResult, error) {
			return hooks.Deny(reason), nil
		},
	})
}

// preLLMMutateHook rewrites the system prompt and model on every pre_llm.
func preLLMMutateHook(system, model string) *hooks.LifecycleRunner {
	return hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "mutate-pre-llm",
		Subscribed: []hooks.Point{hooks.PointPreLLM},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PreLLMPayload)
			clone.System = system
			clone.Model = model
			return hooks.Mutate(&clone), nil
		},
	})
}

// TestTUI_PreLLM_DenyAbortsTurn: a pre_llm deny must stop the turn before
// the provider is ever called, surface the reason as a system block, and
// leave the model idle (not stuck in stateStreaming).
func TestTUI_PreLLM_DenyAbortsTurn(t *testing.T) {
	prov := &captureReqProvider{done: make(chan struct{})}
	m := newLifecycleTestModel(t, prov)
	m.lifecycleHooks = preLLMDenyHook("policy says no")
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hello")}

	cmd := m.startStream()
	if cmd != nil {
		t.Fatalf("a denied pre_llm turn must not return a stream tick cmd")
	}
	// The provider must NOT have been called: prov.done is closed only by
	// StreamTurn. A short window confirms it stays open.
	select {
	case <-prov.done:
		t.Fatal("StreamTurn was called despite a pre_llm deny")
	case <-time.After(100 * time.Millisecond):
	}
	if m.state != stateIdle {
		t.Fatalf("denied turn should return to idle, state=%v", m.state)
	}
	if m.streamCancel != nil {
		t.Fatal("streamCancel should be cleared after a pre_llm deny")
	}
	if !hasSystemBlockContaining(m, "policy says no") {
		t.Fatalf("expected a system block naming the deny reason; blocks=%v", blockKinds(m))
	}
}

// TestTUI_PreLLM_MutateRewritesRequest: a pre_llm mutate rewrites the
// system prompt and model the provider receives this turn. Message history
// is left untouched (prompt-cache invariant) — only req.System / req.Model.
func TestTUI_PreLLM_MutateRewritesRequest(t *testing.T) {
	prov := &captureReqProvider{done: make(chan struct{})}
	m := newLifecycleTestModel(t, prov)
	m.lifecycleHooks = preLLMMutateHook("REWRITTEN SYSTEM", "rewritten-model")
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hello")}

	m.startStream()
	select {
	case <-prov.done:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTurn never called")
	}
	if prov.last.System != "REWRITTEN SYSTEM" {
		t.Fatalf("system not rewritten by pre_llm mutate: %q", prov.last.System)
	}
	if prov.last.Model != "rewritten-model" {
		t.Fatalf("model not rewritten by pre_llm mutate: %q", prov.last.Model)
	}
	// History is immutable at this seam.
	if len(m.msgs) != 1 || m.msgs[0].Role != agent.RoleUser {
		t.Fatalf("pre_llm mutate must not touch message history: %+v", m.msgs)
	}
}

// TestTUI_PostLLM_MutateRewritesAssistantText: a post_llm mutate rewrites
// the assistant text that gets flushed into history, and reconciles the
// already-rendered assistant block so the display matches.
func TestTUI_PostLLM_MutateRewritesAssistantText(t *testing.T) {
	prov := &captureReqProvider{done: make(chan struct{})}
	m := newLifecycleTestModel(t, prov)
	m.lifecycleHooks = hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "mutate-post-llm",
		Subscribed: []hooks.Point{hooks.PointPostLLM},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PostLLMPayload)
			clone.Text = clone.Text + " [reviewed]"
			return hooks.Mutate(&clone), nil
		},
	})
	// Simulate a completed stream: turnText accumulated + a rendered
	// assistant block, as handleStreamEvent would leave it.
	m.turnText = "original answer"
	m.appendBlock(block{kind: "assistant", body: "original answer"})

	m.firePostLLMHook()

	if m.turnText != "original answer [reviewed]" {
		t.Fatalf("post_llm mutate didn't rewrite turnText: %q", m.turnText)
	}
	if got := lastAssistantBody(m); got != "original answer [reviewed]" {
		t.Fatalf("rendered assistant block not reconciled: %q", got)
	}
}

// TestTUI_PostLLM_DenyReplacesAssistantText: a post_llm deny replaces the
// assistant text with the policy reason (the generation already happened,
// so deny is a replace, mirroring agentloop.go).
func TestTUI_PostLLM_DenyReplacesAssistantText(t *testing.T) {
	prov := &captureReqProvider{done: make(chan struct{})}
	m := newLifecycleTestModel(t, prov)
	m.lifecycleHooks = hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "deny-post-llm",
		Subscribed: []hooks.Point{hooks.PointPostLLM},
		Fn: func(_ context.Context, _ hooks.Point, _ hooks.Payload) (hooks.HookResult, error) {
			return hooks.Deny("redacted by policy"), nil
		},
	})
	m.turnText = "secret answer"
	m.appendBlock(block{kind: "assistant", body: "secret answer"})

	m.firePostLLMHook()

	if !strings.Contains(m.turnText, "redacted by policy") {
		t.Fatalf("post_llm deny didn't replace turnText: %q", m.turnText)
	}
	if strings.Contains(m.turnText, "secret answer") {
		t.Fatalf("denied text still leaks the original: %q", m.turnText)
	}
	if strings.Contains(lastAssistantBody(m), "secret answer") {
		t.Fatalf("rendered block still shows the denied original: %q", lastAssistantBody(m))
	}
}

// TestTUI_PostLLM_NoHookIsNoop: with no post_llm hook, firePostLLMHook must
// leave turnText untouched.
func TestTUI_PostLLM_NoHookIsNoop(t *testing.T) {
	prov := &captureReqProvider{done: make(chan struct{})}
	m := newLifecycleTestModel(t, prov)
	m.turnText = "untouched"
	m.firePostLLMHook()
	if m.turnText != "untouched" {
		t.Fatalf("no-op post_llm changed turnText: %q", m.turnText)
	}
}

// helpers -----------------------------------------------------------------
// (hasSystemBlockContaining lives in queue_test.go and is reused here.)

func lastAssistantBody(m *Model) string {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "assistant" {
			return m.blocks[i].body
		}
	}
	return ""
}

func blockKinds(m *Model) []string {
	out := make([]string, 0, len(m.blocks))
	for _, b := range m.blocks {
		out = append(out, b.kind)
	}
	return out
}
