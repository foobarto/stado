package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/mcp"
	"github.com/foobarto/stado/pkg/tool"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type MCPTool struct {
	ServerName string
	Tool       mcpgo.Tool
	Client     *mcp.MCPClient
}

func (t MCPTool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", t.ServerName, t.Tool.Name)
}

func (t MCPTool) Description() string {
	return t.Tool.Description
}

func (t MCPTool) Schema() map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": t.Tool.InputSchema.Properties,
		"required":   t.Tool.InputSchema.Required,
	}
	return schema
}

func (t MCPTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var input map[string]any
	if err := json.Unmarshal(args, &input); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Name = t.Tool.Name
	req.Params.Arguments = input

	result, err := t.Client.Client.CallTool(ctx, req)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	var content string
	for _, c := range result.Content {
		if textContent, ok := c.(mcpgo.TextContent); ok {
			content += textContent.Text + "\n"
		}
	}
	
	return tool.Result{Content: content}, nil
}
