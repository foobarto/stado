package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/mcp"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools/budget"
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

// External MCP tools may execute arbitrary remote or local actions, so
// they are hidden in plan mode by conservatively classifying them as exec.
func (t MCPTool) Class() tool.Class { return tool.ClassExec }

func (t MCPTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	if err := toolinput.CheckLen(len(args)); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

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

	return tool.Result{Content: renderTextContent(result.Content, budget.MCPBytes)}, nil
}

func renderTextContent(contents []mcpgo.Content, maxBytes int) string {
	var b strings.Builder
	total := 0
	for _, c := range contents {
		textContent, ok := c.(mcpgo.TextContent)
		if !ok {
			continue
		}
		total += len(textContent.Text) + 1
		writeBounded(&b, textContent.Text, maxBytes)
		writeBounded(&b, "\n", maxBytes)
	}
	if total <= maxBytes {
		return b.String()
	}
	return truncateKnownTotal(b.String(), total, maxBytes,
		"narrow the MCP tool request")
}

func writeBounded(b *strings.Builder, s string, maxBytes int) {
	if maxBytes <= 0 || b.Len() >= maxBytes {
		return
	}
	remaining := maxBytes - b.Len()
	if len(s) > remaining {
		s = s[:remaining]
	}
	b.WriteString(s)
}

func truncateKnownTotal(head string, total, maxBytes int, hint string) string {
	if len(head) > maxBytes {
		head = head[:maxBytes]
		if i := strings.LastIndexByte(head, '\n'); i > 0 && i > maxBytes-256 {
			head = head[:i]
		}
	}
	marker := fmt.Sprintf("\n[truncated: %d of %d bytes elided",
		total-len(head), total)
	if hint != "" {
		marker += " - " + hint
	}
	marker += "]"
	return head + marker
}
