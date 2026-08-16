package tui

import (
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

func TestTrajectoryInvocationBaseUsesPersistedToolCallOrder(t *testing.T) {
	call := func(id string) agent.Block {
		return agent.Block{ToolUse: &agent.ToolUseBlock{ID: id, Name: "same-tool"}}
	}
	messages := []agent.Message{
		{Role: agent.RoleAssistant, Content: []agent.Block{call("same-tool"), call("same-tool")}},
		{Role: agent.RoleTool},
		{Role: agent.RoleAssistant, Content: []agent.Block{call("same-tool"), call("same-tool")}},
	}
	if got := trajectoryInvocationBase(messages, 2); got != 2 {
		t.Fatalf("invocation base=%d, want 2", got)
	}
	if got := trajectoryInvocationBase(messages[:1], 3); got != 0 {
		t.Fatalf("invalid transcript base=%d, want 0", got)
	}
}
