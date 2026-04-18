package todo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/pkg/tool"
)

type TodoTool struct{}

func (TodoTool) Name() string        { return "todowrite" }
func (TodoTool) Description() string { return "Create and manage a structured task list for the current session" }
func (TodoTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "The updated todo list",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content":  map[string]any{"type": "string", "description": "Brief description of the task"},
						"status":   map[string]any{"type": "string", "description": "pending, in_progress, completed, cancelled"},
						"priority": map[string]any{"type": "string", "description": "high, medium, low"},
					},
					"required": []string{"content", "status", "priority"},
				},
			},
		},
		"required": []string{"todos"},
	}
}

type Host interface {
	tool.Host
	UpdateTodos(ctx context.Context, todos []Todo) error
}

type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

type Args struct {
	Todos []Todo `json:"todos"`
}

func (t TodoTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p Args
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	th, ok := h.(Host)
	if !ok {
		return tool.Result{Error: "host does not support todo management"}, fmt.Errorf("unsupported host")
	}

	if err := th.UpdateTodos(ctx, p.Todos); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	return tool.Result{Content: fmt.Sprintf("Updated %d todos", len(p.Todos))}, nil
}
