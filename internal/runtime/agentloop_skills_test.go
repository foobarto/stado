package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

type skillNamedPluginStub struct{}

func (skillNamedPluginStub) Name() string           { return "skills__load" }
func (skillNamedPluginStub) Description() string    { return "plugin-shaped test tool" }
func (skillNamedPluginStub) Schema() map[string]any { return map[string]any{"type": "object"} }
func (skillNamedPluginStub) Run(context.Context, json.RawMessage, pkgtool.Host) (pkgtool.Result, error) {
	return pkgtool.Result{Content: `{"name":"recon","loaded":true,"body":"ordinary tool body"}`}, nil
}

type skillOriginCaptureProvider struct{ turn int }

func (p *skillOriginCaptureProvider) Name() string                     { return "skill-origin-capture" }
func (p *skillOriginCaptureProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *skillOriginCaptureProvider) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	p.turn++
	ch := make(chan agent.Event, 3)
	if p.turn == 1 {
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{ID: "load-1", Name: "skills__load", Input: json.RawMessage(`{}`)}}
	}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

func TestAgentLoopDoesNotPrivilegeSkillNamedToolResultIntoUserRole(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(skillNamedPluginStub{})
	provider := &skillOriginCaptureProvider{}
	_, messages, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: provider, Executor: &tools.Executor{Registry: reg}, Model: "m",
		Config:   &config.Config{Tools: config.Tools{Autoload: []string{"skills__load"}}},
		Skills:   []skills.Skill{{Name: "recon", Body: "native body must not be injected"}},
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")}, MaxTurns: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	userMessages := 0
	var toolContent string
	for _, message := range messages {
		if message.Role == agent.RoleUser {
			userMessages++
		}
		if message.Role == agent.RoleTool && len(message.Content) > 0 && message.Content[0].ToolResult != nil {
			toolContent = message.Content[0].ToolResult.Content
		}
	}
	if userMessages != 1 {
		t.Fatalf("model tool result synthesized a user role; user messages=%d", userMessages)
	}
	if toolContent != `{"name":"recon","loaded":true,"body":"ordinary tool body"}` {
		t.Fatalf("tool result was rewritten: %q", toolContent)
	}
}
