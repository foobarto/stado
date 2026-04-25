package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/pkg/agent"
)

func TestSpawnAgentToolResultAddsVisibleChildNotice(t *testing.T) {
	m := scenarioModel(t)
	m.blocks = append(m.blocks, block{
		kind:     "tool",
		toolID:   "spawn-1",
		toolName: subagent.ToolName,
	})

	content := `{
  "status": "timeout",
  "role": "explorer",
  "mode": "read_only",
  "child_session": "child-123",
  "worktree": "/tmp/stado-child-123",
  "error": "child timed out after 1 second(s)"
}`
	_, _ = m.Update(toolResultMsg{result: agent.ToolResultBlock{
		ToolUseID: "spawn-1",
		Content:   content,
	}})

	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" {
		t.Fatalf("last block kind = %q, want system", last.kind)
	}
	for _, want := range []string{
		"spawn_agent timeout",
		"child-123",
		"child timed out",
		"stado session attach child-123",
	} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("notice missing %q:\n%s", want, last.body)
		}
	}
}

func TestSpawnAgentWorkerResultAddsAdoptionHint(t *testing.T) {
	m := scenarioModel(t)
	m.blocks = append(m.blocks, block{
		kind:     "tool",
		toolID:   "spawn-1",
		toolName: subagent.ToolName,
	})

	content := `{
  "status": "completed",
  "role": "worker",
  "mode": "workspace_write",
  "child_session": "child-456",
  "worktree": "/tmp/stado-child-456",
  "fork_tree": "0123456789abcdef0123456789abcdef01234567",
  "changed_files": ["docs/a.md", "docs/b.md"],
  "scope_violations": ["blocked.txt: outside write_scope"]
}`
	_, _ = m.Update(toolResultMsg{result: agent.ToolResultBlock{
		ToolUseID: "spawn-1",
		Content:   content,
	}})

	last := m.blocks[len(m.blocks)-1]
	for _, want := range []string{
		"changed: 2 file(s)",
		"scope violations: 1",
		"stado session adopt",
		"child-456",
		"--fork-tree 0123456789abcdef0123456789abcdef01234567 --apply",
	} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("notice missing %q:\n%s", want, last.body)
		}
	}
}
