package task

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/pkg/tool"
)

type TaskTool struct {
	Runner func(ctx context.Context, description, prompt string) (string, error)
}

func (TaskTool) Name() string        { return "task" }
func (TaskTool) Description() string { return "Launch a new agent to handle complex, multistep tasks autonomously." }
func (TaskTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description":   map[string]any{"type": "string", "description": "A short (3-5 words) description of the task"},
			"prompt":        map[string]any{"type": "string", "description": "The task for the agent to perform"},
			"subagent_type": map[string]any{"type": "string", "description": "The type of specialized agent to use (explore, general)"},
		},
		"required": []string{"description", "prompt", "subagent_type"},
	}
}

type Args struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
}

func (t TaskTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p Args
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	approval, err := h.Approve(ctx, tool.ApprovalRequest{
		Tool:    "task",
		Command: fmt.Sprintf("Spawn subagent (%s): %s", p.SubagentType, p.Description),
	})
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if approval == tool.DecisionDeny {
		return tool.Result{Error: "task execution denied by user"}, nil
	}

	result, err := t.Runner(ctx, p.Description, p.Prompt)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	
	return tool.Result{Content: result}, nil
}
