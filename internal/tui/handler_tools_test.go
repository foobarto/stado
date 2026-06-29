package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
)

// noopProvider satisfies startStream after onToolsExecuted without a real API.
type noopProvider struct{}

func (noopProvider) Name() string                     { return "noop" }
func (noopProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (noopProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

// TestOnToolsExecuted_InjectsSkillLoadBody: regression for the TUI bug where
// onToolResult trimmed skills__load results before storing them in
// pendingResults, so absorbSkillLoads in onToolsExecuted had nothing to inject.
func TestOnToolsExecuted_InjectsSkillLoadBody(t *testing.T) {
	body := "Reproduce the bug first, then fix."
	raw, err := json.Marshal(runtime.SkillLoadResponse{
		Name: "bugfix", Body: body, Loaded: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	m := newSkillModel(t, t.TempDir())
	m.executor = &tools.Executor{Registry: runtime.BuildDefaultRegistry(nil)}
	m.provider = noopProvider{}
	startMsgs := len(m.msgs)
	startBlocks := len(m.blocks)

	_, _ = onToolsExecuted(m, toolsExecutedMsg{results: []agent.ToolResultBlock{
		{ToolUseID: "t1", Content: string(raw)},
	}})

	if len(m.msgs) != startMsgs+2 {
		t.Fatalf("expected tool + user injection msgs; got %d new (total %d)", len(m.msgs)-startMsgs, len(m.msgs))
	}
	if m.msgs[startMsgs].Role != agent.RoleTool {
		t.Fatalf("first new msg should be role=tool, got %v", m.msgs[startMsgs].Role)
	}
	if m.msgs[startMsgs+1].Role != agent.RoleUser {
		t.Fatalf("second new msg should be role=user (skill injection), got %v", m.msgs[startMsgs+1].Role)
	}
	injected := m.msgs[startMsgs+1].Content[0].Text.Text
	if injected != body {
		t.Fatalf("injected body = %q, want %q", injected, body)
	}
	// Persisted tool result should be trimmed (body lives in the user msg).
	toolContent := m.msgs[startMsgs].Content[0].ToolResult.Content
	if strings.Contains(toolContent, body) {
		t.Errorf("tool_result should not retain full body; got %q", toolContent)
	}
	if !strings.Contains(toolContent, `"loaded":true`) {
		t.Errorf("trimmed tool_result should confirm load; got %q", toolContent)
	}

	var injectedBlock bool
	for i := startBlocks; i < len(m.blocks); i++ {
		if m.blocks[i].kind == "user" && m.blocks[i].body == body {
			injectedBlock = true
			break
		}
	}
	if !injectedBlock {
		t.Fatalf("expected a user block with injected skill body among %d new blocks", len(m.blocks)-startBlocks)
	}
}

// TestSkillModelInvocation_SessionDisableSuppressesListing: in-session
// /tool disable skills__load must hide both the autoload and the listing.
func TestSkillModelInvocation_SessionDisableSuppressesListing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".stado", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tidy.md"), []byte(`---
name: tidy
description: tidy imports
---
Sort imports.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newSkillModel(t, root)
	m.executor = &tools.Executor{Registry: runtime.BuildDefaultRegistry(nil)}
	m.sessionToolOverrides.disableAdd = []string{"skills__load"}

	if m.skillModelInvocationEnabled() {
		t.Fatal("session-disabled skills__load should disable model invocation gate")
	}
	sys := m.turnSystemPrompt("hi")
	if strings.Contains(sys, "Available skills") {
		t.Errorf("listing should be suppressed when skills__load is session-disabled:\n%s", sys)
	}
}
