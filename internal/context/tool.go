package context

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/foobarto/stado/pkg/tool"
)

type ContextTool struct {
	Engine *Engine
}

func (ContextTool) Name() string        { return "context" }
func (ContextTool) Description() string { return "Search the project context for relevant files and symbols" }
func (ContextTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query for files, symbols, or code content"},
		},
		"required": []string{"query"},
	}
}

type Args struct {
	Query string `json:"query"`
}

func (t ContextTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p Args
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	results, err := t.Engine.Search(p.Query, 5)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	if len(results) == 0 {
		return tool.Result{Content: "No relevant context found for query: " + p.Query}, nil
	}

	return tool.Result{Content: strings.Join(results, "\n\n---\n\n")}, nil
}
