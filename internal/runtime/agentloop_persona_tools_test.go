package runtime

import (
	"context"
	"testing"

	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

// toolCaptureProvider records the tool surface sent on the first turn.
type toolCaptureProvider struct {
	tools []agent.ToolDef
}

func (p *toolCaptureProvider) Name() string                     { return "capture" }
func (p *toolCaptureProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p *toolCaptureProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.tools = req.Tools
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

// TestAgentLoop_PromotesPersonaToolsToSurface: a headless run with an
// active persona promotes the persona's EffectiveTools() into the per-turn
// autoload surface (additive — the default core stays). Reproduce-first
// for the run/headless acceptance criterion.
func TestAgentLoop_PromotesPersonaToolsToSurface(t *testing.T) {
	reg := BuildDefaultRegistry(nil)

	// A tool registered but NOT in the default autoload core.
	base := AutoloadedTools(reg, nil)
	baseNames := map[string]bool{}
	for _, tl := range base {
		baseNames[tl.Name()] = true
	}
	var promote string
	for _, tl := range reg.All() {
		if !baseNames[tl.Name()] && !IsMetaTool(tl.Name()) {
			promote = tl.Name()
			break
		}
	}
	if promote == "" {
		t.Skip("no non-core registered tool available to test promotion")
	}

	prov := &toolCaptureProvider{}
	exec := &tools.Executor{Registry: reg}
	persona := &personas.Persona{Name: "tooled", Tools: []string{promote}}

	if _, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Executor: exec,
		Persona:  persona,
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 1,
	}); err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}

	got := map[string]bool{}
	for _, td := range prov.tools {
		got[td.Name] = true
	}
	if !got[promote] {
		t.Errorf("persona tool %q should be promoted into the run's tool surface; got %d tools", promote, len(prov.tools))
	}
	// Additive: a default-core tool must still be present.
	for name := range baseNames {
		if !got[name] {
			t.Errorf("promotion must be additive; default core %q missing from surface", name)
		}
	}
}
