package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

type countingSkillNamedPluginStub struct{ runs int }

func (*countingSkillNamedPluginStub) Name() string           { return "skills__load" }
func (*countingSkillNamedPluginStub) Description() string    { return "counting plugin-shaped test tool" }
func (*countingSkillNamedPluginStub) Schema() map[string]any { return map[string]any{"type": "object"} }
func (s *countingSkillNamedPluginStub) Run(context.Context, json.RawMessage, pkgtool.Host) (pkgtool.Result, error) {
	s.runs++
	return pkgtool.Result{Content: `{"loaded":true}`}, nil
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

type duplicateCallIDProvider struct{ turn int }

func (p *duplicateCallIDProvider) Name() string                     { return "duplicate-call-id" }
func (p *duplicateCallIDProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *duplicateCallIDProvider) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	p.turn++
	ch := make(chan agent.Event, 2)
	if p.turn <= 2 {
		input := json.RawMessage(fmt.Sprintf(`{"n":%d}`, p.turn))
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{ID: "skills__load", Name: "skills__load", Input: input}}
	}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

func TestAgentLoopToolOutcomeUsesProviderOrderNotCallID(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(skillNamedPluginStub{})
	var invocations []int
	var durableCounts []int
	var durableAtOutcome []int
	priorCall := agent.Block{ToolUse: &agent.ToolUseBlock{ID: "skills__load", Name: "skills__load"}}
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &duplicateCallIDProvider{}, Executor: &tools.Executor{Registry: reg}, Model: "m",
		Config: &config.Config{Tools: config.Tools{Autoload: []string{"skills__load"}}},
		Messages: []agent.Message{
			agent.Text(agent.RoleUser, "prior"),
			{Role: agent.RoleAssistant, Content: []agent.Block{priorCall, priorCall}},
			{Role: agent.RoleTool},
			agent.Text(agent.RoleUser, "resume"),
		},
		MaxTurns:                    3,
		InitialTrajectoryInvocation: 4,
		BeforeToolExecution: func(messages []agent.Message) error {
			calls := 0
			for _, message := range messages {
				for _, block := range message.Content {
					if block.ToolUse != nil {
						calls++
					}
				}
			}
			durableCounts = append(durableCounts, calls)
			return nil
		},
		OnToolOutcome: func(_, invocation int, _ agent.ToolUseBlock, _ agent.ToolResultBlock) {
			invocations = append(invocations, invocation)
			durableAtOutcome = append(durableAtOutcome, durableCounts[len(durableCounts)-1])
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 2 || invocations[0] != 4 || invocations[1] != 5 {
		t.Fatalf("invocations=%v, want [4 5]", invocations)
	}
	if len(durableAtOutcome) != 2 || durableAtOutcome[0] != 3 || durableAtOutcome[1] != 4 {
		t.Fatalf("durable calls at outcome=%v, want [3 4]", durableAtOutcome)
	}
}

func TestAgentLoopPersistenceFailureAbortsBeforeToolExecution(t *testing.T) {
	reg := tools.NewRegistry()
	counting := &countingSkillNamedPluginStub{}
	reg.Register(counting)
	outcomes := 0
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &duplicateCallIDProvider{}, Executor: &tools.Executor{Registry: reg}, Model: "m",
		Config:   &config.Config{Tools: config.Tools{Autoload: []string{"skills__load"}}},
		Messages: []agent.Message{agent.Text(agent.RoleUser, "hi")}, MaxTurns: 2,
		BeforeToolExecution: func([]agent.Message) error { return fmt.Errorf("disk full") },
		OnToolOutcome:       func(int, int, agent.ToolUseBlock, agent.ToolResultBlock) { outcomes++ },
	})
	if err == nil || !strings.Contains(err.Error(), "persist tool-call evidence: disk full") {
		t.Fatalf("error=%v, want persistence failure", err)
	}
	if counting.runs != 0 || outcomes != 0 {
		t.Fatalf("tool runs=%d outcomes=%d, want zero before persistence", counting.runs, outcomes)
	}
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
