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

func TestConversation_FilePermissionsArePrivate(t *testing.T) {
	wt := t.TempDir()
	if err := AppendMessage(wt, agent.Text(agent.RoleUser, "secret")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(wt, ConversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("conversation mode = %#o, want 0600", got)
	}
}

func TestConversationAppendRejectsOversizedRecord(t *testing.T) {
	wt := t.TempDir()
	err := AppendMessage(wt, agent.Text(agent.RoleUser, strings.Repeat("x", int(maxConversationRecordBytes)+1)))
	if err == nil || !strings.Contains(err.Error(), "record exceeds") {
		t.Fatalf("expected oversized record error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(wt, ConversationFile)); !os.IsNotExist(statErr) {
		t.Fatalf("conversation log should not be created for oversized record, stat err = %v", statErr)
	}
}

func TestConversationAppendRejectsOversizedLog(t *testing.T) {
	wt := t.TempDir()
	stadoDir := filepath.Join(wt, ".stado")
	if err := os.MkdirAll(stadoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wt, ConversationFile)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxConversationLogBytes-4); err != nil {
		t.Fatal(err)
	}

	err := AppendMessage(wt, agent.Text(agent.RoleUser, "full"))
	if err == nil || !strings.Contains(err.Error(), "conversation log exceeds") {
		t.Fatalf("expected oversized log error, got %v", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Size() != maxConversationLogBytes-4 {
		t.Fatalf("conversation log size = %d, want %d", info.Size(), maxConversationLogBytes-4)
	}
}

func TestConversationAppendRejectsLogSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".stado"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wt, ConversationFile)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := AppendMessage(wt, agent.Text(agent.RoleUser, "outside"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected conversation log symlink error, got %v", err)
	}
	got, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "do not touch\n" {
		t.Fatalf("outside conversation log was modified: %q", got)
	}
}

func TestConversationLoadRejectsLogSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte(`{"role":"user","content":[{"type":"text","text":"outside"}]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".stado"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wt, ConversationFile)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := LoadConversation(wt); err == nil {
		t.Fatal("LoadConversation should reject a conversation log symlink that escapes the worktree")
	}
}

func TestRawConversationLogRejectsLogSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte(`{"role":"user","content":[{"type":"text","text":"outside"}]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".stado"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wt, ConversationFile)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := RawConversationLog(wt); err == nil {
		t.Fatal("RawConversationLog should reject a conversation log symlink that escapes the worktree")
	}
}

func TestRawConversationLogRejectsOversizedLog(t *testing.T) {
	wt := t.TempDir()
	stadoDir := filepath.Join(wt, ".stado")
	if err := os.MkdirAll(stadoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wt, ConversationFile)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxConversationLogBytes+1); err != nil {
		t.Fatal(err)
	}

	if _, err := RawConversationLog(wt); err == nil {
		t.Fatal("RawConversationLog should reject oversized logs")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want size limit", err)
	}
}

func TestConversationAppendRejectsStadoDirSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	wt := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(wt, ".stado")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := AppendMessage(wt, agent.Text(agent.RoleUser, "outside")); err == nil {
		t.Fatal("AppendMessage should reject a .stado symlink that escapes the worktree")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, filepath.Base(ConversationFile))); !os.IsNotExist(err) {
		t.Fatalf("outside conversation log was created or stat failed unexpectedly: %v", err)
	}
}

func TestConversationWorktreeRootSymlinkRejected(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "worktree-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := AppendMessage(link, agent.Text(agent.RoleUser, "escape")); err == nil {
		t.Fatal("AppendMessage should reject a symlinked worktree root")
	}
	if _, err := os.Stat(filepath.Join(target, ConversationFile)); !os.IsNotExist(err) {
		t.Fatalf("symlink target conversation log was modified, stat err = %v", err)
	}
}

func TestConversation_CompactionEventIsAppendOnlyAndFoldsView(t *testing.T) {
	wt := t.TempDir()
	for _, text := range []string{"turn 1", "turn 2", "turn 3"} {
		if err := AppendMessage(wt, agent.Text(agent.RoleUser, text)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(wt, ConversationFile))
	if err != nil {
		t.Fatal(err)
	}
	rawSHA, err := ConversationLogSHA(wt)
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendCompaction(wt, ConversationCompaction{
		Summary:    "summary of turns 1-3",
		FromTurn:   0,
		ToTurn:     3,
		TurnsTotal: 3,
		RawLogSHA:  rawSHA,
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConversation(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded messages = %d, want compacted view of 1", len(loaded))
	}
	if got := loaded[0].Content[0].Text.Text; !strings.Contains(got, "summary of turns 1-3") {
		t.Fatalf("compacted view missing summary: %q", got)
	}
	after, err := os.ReadFile(filepath.Join(wt, ConversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after), string(before)) {
		t.Fatal("conversation log was not append-only; original prefix changed")
	}
	if !strings.Contains(string(after), "turn 1") || !strings.Contains(string(after), `"type":"compaction"`) {
		t.Fatalf("raw log should retain original turns and compaction event: %s", after)
	}
}

func TestWriteConversationRefusesNonEmptyLog(t *testing.T) {
	wt := t.TempDir()
	if err := AppendMessage(wt, agent.Text(agent.RoleUser, "existing")); err != nil {
		t.Fatal(err)
	}
	err := WriteConversation(wt, []agent.Message{agent.Text(agent.RoleAssistant, "replacement")})
	if err == nil {
		t.Fatal("expected WriteConversation to refuse replacing a non-empty append-only log")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("error should mention append-only log, got %v", err)
	}
	loaded, err := LoadConversation(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Content[0].Text.Text != "existing" {
		t.Fatalf("conversation was rewritten despite refusal: %+v", loaded)
	}
}

func TestWriteConversationDoesNotFollowPredictableTempSymlink(t *testing.T) {
	wt := t.TempDir()
	stadoDir := filepath.Join(wt, ".stado")
	if err := os.MkdirAll(stadoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(stadoDir, "decoy.jsonl")
	if err := os.WriteFile(decoy, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("decoy.jsonl", filepath.Join(stadoDir, filepath.Base(ConversationFile)+".tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := WriteConversation(wt, []agent.Message{agent.Text(agent.RoleUser, "seeded")}); err != nil {
		t.Fatalf("WriteConversation: %v", err)
	}

	decoyData, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoyData) != "do not replace\n" {
		t.Fatalf("predictable temp symlink target was modified: %q", decoyData)
	}
	info, err := os.Lstat(filepath.Join(wt, ConversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("conversation log was replaced by the pre-created temp symlink")
	}
	loaded, err := LoadConversation(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Content[0].Text.Text != "seeded" {
		t.Fatalf("seeded conversation not loaded: %+v", loaded)
	}
}
