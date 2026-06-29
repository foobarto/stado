package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// probeTool is registered but outside the default autoload core — used to
// verify skills__load allowed-tools promotion on headless/run surfaces.
type probeTool struct{}

func (probeTool) Name() string        { return "probe__extra" }
func (probeTool) Description() string { return "test-only probe" }
func (probeTool) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
func (probeTool) Run(context.Context, json.RawMessage, pkgtool.Host) (pkgtool.Result, error) {
	return pkgtool.Result{Content: "ok"}, nil
}

// skillSurfaceCaptureProvider replays a skills__load turn then records the
// tool surface on the following turn.
type skillSurfaceCaptureProvider struct {
	turn    int
	surface []agent.ToolDef
}

func (p *skillSurfaceCaptureProvider) Name() string { return "skill-surface-capture" }
func (p *skillSurfaceCaptureProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *skillSurfaceCaptureProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.turn++
	ch := make(chan agent.Event, 4)
	if p.turn == 1 {
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID: "load-1", Name: "skills__load", Input: json.RawMessage(`{"name":"recon"}`),
		}}
	} else {
		p.surface = req.Tools
	}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

// TestAgentLoop_SkillLoadActivatesAllowedTools: persona-scoped allowed-tools
// from skills__load join the per-turn surface on the next headless turn
// (same promotion path as tools__describe, via activatedNames).
func TestAgentLoop_SkillLoadActivatesAllowedTools(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	reg.Register(probeTool{})

	catalog := []skills.Skill{{
		Name:         "recon",
		Body:         "look around",
		Scope:        skills.ScopePersona,
		AllowedTools: []string{"probe__extra"},
	}}

	prov := &skillSurfaceCaptureProvider{}
	exec := &tools.Executor{Registry: reg}

	_, msgs, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: prov,
		Executor: exec,
		Model:    "m",
		Skills:   catalog,
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}

	got := map[string]bool{}
	for _, td := range prov.surface {
		got[td.Name] = true
	}
	if !got["probe__extra"] {
		t.Errorf("skills__load allowed-tools should promote probe__extra onto turn-2 surface; got %v", toolNames(prov.surface))
	}
	if !got["skills__load"] {
		t.Error("skills__load should remain on the surface when model-visible skills exist")
	}

	// Skill body injected as a user message after the tool-result block.
	var injected bool
	for _, msg := range msgs {
		if msg.Role != agent.RoleUser {
			continue
		}
		for _, b := range msg.Content {
			if b.Text != nil && b.Text.Text == "look around" {
				injected = true
			}
		}
	}
	if !injected {
		t.Error("skills__load body should be injected as a user message")
	}
}

func toolNames(defs []agent.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}
