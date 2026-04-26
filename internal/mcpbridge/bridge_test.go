package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools/budget"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestRenderTextContentPreservesTextBlocks(t *testing.T) {
	got := renderTextContent([]mcpgo.Content{
		mcpgo.TextContent{Type: "text", Text: "one"},
		mcpgo.ImageContent{Type: "image", Data: "ignored"},
		mcpgo.TextContent{Type: "text", Text: "two"},
	}, budget.MCPBytes)
	if got != "one\ntwo\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestRenderTextContentCapsOutput(t *testing.T) {
	first := "prefix\n"
	huge := strings.Repeat("x", budget.MCPBytes+4096)
	got := renderTextContent([]mcpgo.Content{
		mcpgo.TextContent{Type: "text", Text: strings.TrimSuffix(first, "\n")},
		mcpgo.TextContent{Type: "text", Text: huge},
	}, budget.MCPBytes)

	if len(got) > budget.MCPBytes+256 {
		t.Fatalf("content length = %d, want near cap", len(got))
	}
	if !strings.HasPrefix(got, first) {
		t.Fatalf("prefix not preserved: %q", got[:min(len(got), len(first)+8)])
	}
	if !strings.Contains(got, "[truncated:") {
		t.Fatalf("truncation marker missing")
	}
	total := len(first) + len(huge) + 1
	if !strings.Contains(got, fmt.Sprintf("of %d bytes elided", total)) {
		t.Fatalf("full size not surfaced: %q", got[len(got)-128:])
	}
}

func TestMCPToolRunRejectsOversizedArgsBeforeDecode(t *testing.T) {
	_, err := (MCPTool{}).Run(context.Background(),
		json.RawMessage(strings.Repeat("x", toolinput.MaxBytes+1)), nil)
	if err == nil {
		t.Fatal("expected oversized args to fail")
	}
	if !strings.Contains(err.Error(), "tool input exceeds") {
		t.Fatalf("error = %v", err)
	}
}
