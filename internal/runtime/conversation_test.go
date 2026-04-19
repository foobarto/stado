package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestConversation_RoundTrip covers the straightforward append-then-
// load flow: three messages go in, three come out in order, with
// Content intact. The user/assistant/tool role mix exercises the
// serialisation shape that matters for resume.
func TestConversation_RoundTrip(t *testing.T) {
	wt := t.TempDir()

	msgs := []agent.Message{
		agent.Text(agent.RoleUser, "fix the bug"),
		agent.Text(agent.RoleAssistant, "I'll start by looking at the test"),
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{
				ToolUseID: "tc_1",
				Content:   "output of grep command",
			}},
		}},
	}
	for _, m := range msgs {
		if err := AppendMessage(wt, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	loaded, err := LoadConversation(wt)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(loaded) != len(msgs) {
		t.Fatalf("round-trip count: got %d, want %d", len(loaded), len(msgs))
	}
	for i, m := range msgs {
		if loaded[i].Role != m.Role {
			t.Errorf("msgs[%d].Role = %q, want %q", i, loaded[i].Role, m.Role)
		}
	}
	// Spot-check the tool-result content survives.
	if loaded[2].Content[0].ToolResult == nil ||
		loaded[2].Content[0].ToolResult.Content != "output of grep command" {
		t.Errorf("tool-result round-trip mangled: %+v", loaded[2])
	}
}

// TestConversation_MissingFile_ReturnsNil: a worktree that was never
// typed in yields (nil, nil). Callers treat this as "fresh session"
// with no special case.
func TestConversation_MissingFile_ReturnsNil(t *testing.T) {
	wt := t.TempDir()
	msgs, err := LoadConversation(wt)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil messages, got %+v", msgs)
	}
}

// TestConversation_EmptyWorktreeArg: defensive — an empty worktree
// string in either direction should no-op cleanly rather than panic.
func TestConversation_EmptyWorktreeArg(t *testing.T) {
	if err := AppendMessage("", agent.Text(agent.RoleUser, "x")); err == nil {
		t.Error("AppendMessage with empty worktree should error")
	}
	msgs, err := LoadConversation("")
	if err != nil {
		t.Errorf("LoadConversation with empty worktree should not error: %v", err)
	}
	if msgs != nil {
		t.Error("LoadConversation with empty worktree should return nil slice")
	}
}

// TestConversation_PartialTail_Tolerated: kill stado mid-write and
// the conversation file has a half-encoded trailing line. The loader
// should return every complete line and silently stop at the
// truncation — losing one message is better than refusing to boot.
func TestConversation_PartialTail_Tolerated(t *testing.T) {
	wt := t.TempDir()
	if err := AppendMessage(wt, agent.Text(agent.RoleUser, "one")); err != nil {
		t.Fatal(err)
	}
	if err := AppendMessage(wt, agent.Text(agent.RoleUser, "two")); err != nil {
		t.Fatal(err)
	}
	// Manually corrupt the file by appending half a JSON object.
	path := filepath.Join(wt, ConversationFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(`{"role":"user","content":[`))
	_ = f.Close()

	loaded, err := LoadConversation(wt)
	if err != nil {
		t.Fatalf("expected successful load with partial tail: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("got %d messages, want 2 (partial tail should be skipped)", len(loaded))
	}
}

// TestConversation_PreservesUnicodeAndTags: JSON encoders default to
// HTML-escaping which would break content containing <, >, &. We set
// SetEscapeHTML(false) because tool output contains those chars
// routinely (HTML fetch, grep hits, etc.). Assert the raw file
// doesn't carry \u003c garbage.
func TestConversation_PreservesUnicodeAndTags(t *testing.T) {
	wt := t.TempDir()
	m := agent.Text(agent.RoleUser, "look at <div>hi</div> & find results")
	if err := AppendMessage(wt, m); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(wt, ConversationFile))
	if strings.Contains(string(raw), `\u003c`) {
		t.Errorf("HTML escape slipped through: %q", raw)
	}
	if !strings.Contains(string(raw), "<div>") {
		t.Errorf("literal <div> missing: %q", raw)
	}
}
