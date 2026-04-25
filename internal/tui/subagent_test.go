package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
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

func TestSubagentEventsRenderSidebarActivity(t *testing.T) {
	m := describeSlashModel(t)
	child := "123456789abcdef"

	_, _ = m.Update(subagentEventMsg{ev: runtime.SubagentEvent{
		Phase:         "started",
		ParentSession: m.session.ID,
		ChildSession:  child,
		Role:          "worker",
		Mode:          "workspace_write",
		Status:        "running",
	}})

	got := m.renderSidebar(70)
	for _, want := range []string{
		"Subagents",
		"12345678 running worker/workspace_write",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sidebar missing %q:\n%s", want, got)
		}
	}

	_, _ = m.Update(subagentEventMsg{ev: runtime.SubagentEvent{
		Phase:           "finished",
		ParentSession:   m.session.ID,
		ChildSession:    child,
		Role:            "worker",
		Mode:            "workspace_write",
		Status:          "completed",
		ForkTree:        "0123456789abcdef0123456789abcdef01234567",
		ChangedFiles:    []string{"docs/a.md", "docs/b.md"},
		ScopeViolations: []string{"blocked.txt: outside write_scope"},
	}})

	got = m.renderSidebar(70)
	for _, want := range []string{
		"12345678 completed worker/workspace_write",
		"2 changed",
		"adopt ready",
		"1 scope violation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sidebar missing %q:\n%s", want, got)
		}
	}
	if len(m.subagents) != 1 || !strings.Contains(m.subagents[0].AdoptionCommand, "stado session adopt") {
		t.Fatalf("adoption command not tracked: %#v", m.subagents)
	}
}

func TestSubagentsSlashRendersActivityOverview(t *testing.T) {
	m := scenarioModel(t)
	child := "123456789abcdef"

	m.recordSubagentEvent(runtime.SubagentEvent{
		Phase:           "finished",
		ParentSession:   "parent-1",
		ChildSession:    child,
		Worktree:        "/tmp/stado-child",
		Role:            "worker",
		Mode:            "workspace_write",
		Status:          "completed",
		ForkTree:        "0123456789abcdef0123456789abcdef01234567",
		ChangedFiles:    []string{"docs/a.md", "docs/b.md"},
		ScopeViolations: []string{"blocked.txt: outside write_scope"},
	})

	_ = m.handleSlash("/subagents")
	last := m.blocks[len(m.blocks)-1]
	for _, want := range []string{
		"Subagents:",
		child,
		"completed  worker/workspace_write",
		"worktree: /tmp/stado-child",
		"changed files: 2",
		"scope violations: 1",
		"stado session adopt parent-1 123456789abcdef",
		"--fork-tree 0123456789abcdef0123456789abcdef01234567 --apply",
	} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("/subagents overview missing %q:\n%s", want, last.body)
		}
	}
}

func TestSubagentsSlashEmpty(t *testing.T) {
	m := scenarioModel(t)
	got := m.renderSubagentsOverview()
	if !strings.Contains(got, "no subagent activity yet") {
		t.Fatalf("empty subagent overview = %q", got)
	}
}
