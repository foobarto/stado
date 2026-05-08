package tui

import (
	"strings"
	"testing"
)

// TestToolSlash_NoArgsShowsUsage: bare /tool prints a usage hint
// pointing operators at /tools for discovery. /t mirrors. F-tool.
func TestToolSlash_NoArgsShowsUsage(t *testing.T) {
	for _, prefix := range []string{"/tool", "/t"} {
		t.Run(prefix, func(t *testing.T) {
			m := newPickerTestModel(t, "anthropic")
			m.handleSlash(prefix)
			body := m.blocks[len(m.blocks)-1].body
			if !strings.Contains(body, "usage:") {
				t.Errorf("expected usage hint, got %q", body)
			}
			if !strings.Contains(body, "/tool <name>") {
				t.Errorf("usage hint should show /tool <name>, got %q", body)
			}
		})
	}
}

// TestToolSlash_UnknownToolReportsCleanly: /tool with an unknown
// name surfaces a clear error pointing at /tools, not a stack trace
// or silent no-op. F-tool.
func TestToolSlash_UnknownToolReportsCleanly(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/tool nope.does_not_exist {}")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "not found") {
		t.Errorf("expected not-found error, got %q", body)
	}
}

// TestToolSlash_RefusesPTYBoundShellTools: same gate as
// `stado tool run`. /tool fs.read works; /tool shell.spawn surfaces
// the advisory pointing at the agent loop / MCP. F-tool / B5.
func TestToolSlash_RefusesPTYBoundShellTools(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/tool shell.spawn {}")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "PTY-bound") {
		t.Errorf("expected PTY-bound advisory, got %q", body)
	}
	if !strings.Contains(body, "agent loop") {
		t.Errorf("advisory should mention the agent loop, got %q", body)
	}
}

// TestToolSlash_TAliasFlowsThroughSamePath: /t fs.read … hits the
// same handler as /tool fs.read … . Asserted by checking the same
// usage / not-found surface — the dispatch path is shared. F-tool.
func TestToolSlash_TAliasFlowsThroughSamePath(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.handleSlash("/t shell.list {}")
	body := m.blocks[len(m.blocks)-1].body
	if !strings.Contains(body, "PTY-bound") {
		t.Errorf("/t alias should hit the same PTY gate as /tool, got %q", body)
	}
}

// TestToolSlash_ManagementVerbsRouteToManager: reserved verbs
// (ls/info/enable/disable/etc.) must flow to the management handler
// rather than being treated as tool names. F-tool.
func TestToolSlash_ManagementVerbsRouteToManager(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// /tool ls produces the existing management surface (a list of
	// tools, not a "not found" error). Substring assertion only —
	// we don't pin the exact list shape.
	m.handleSlash("/tool ls")
	body := m.blocks[len(m.blocks)-1].body
	if strings.Contains(body, "not found") {
		t.Errorf("/tool ls must hit the management handler, got %q", body)
	}
}
