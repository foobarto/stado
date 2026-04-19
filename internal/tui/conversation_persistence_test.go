package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// newPersistTestModel wires a Model against a real stadogit.Session
// with its worktree under t.TempDir, so conversation.jsonl actually
// lands on disk.
func newPersistTestModel(t *testing.T) *Model {
	t.Helper()
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)

	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, base)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, worktreeRoot, "persist-sess", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	rnd, _ := render.New(theme.Default())
	reg := keys.NewRegistry()
	m := NewModel(base, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.session = sess
	m.width, m.height = 80, 24
	return m
}

// TestAppendUser_PersistsToDisk: the most-used append site must hit
// conversation.jsonl. Earlier we only appended to m.msgs in memory;
// this regresses if someone bypasses persistMessage.
func TestAppendUser_PersistsToDisk(t *testing.T) {
	m := newPersistTestModel(t)
	m.appendUser("hi there")

	loaded, err := runtime.LoadConversation(m.session.WorktreePath)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 message on disk, got %d: %+v", len(loaded), loaded)
	}
	if loaded[0].Role != agent.RoleUser {
		t.Errorf("role = %q, want user", loaded[0].Role)
	}
	if loaded[0].Content[0].Text == nil || loaded[0].Content[0].Text.Text != "hi there" {
		t.Errorf("text body not preserved: %+v", loaded[0])
	}
}

// TestAppendUser_NoSessionIsSafe: tests + headless bootstrap may
// construct Models without sessions. persistMessage must no-op
// gracefully — otherwise every non-session test would need mocking.
func TestAppendUser_NoSessionIsSafe(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	// m.session left nil deliberately.
	m.appendUser("x")
	if len(m.msgs) != 1 {
		t.Errorf("m.msgs should still update even without session: got %d", len(m.msgs))
	}
}

// TestLoadPersistedConversation_RestoresMsgsAndBlocks: simulate a
// "prior session" by seeding conversation.jsonl on disk, then boot
// a fresh Model over the same worktree and assert LoadPersistedConversation
// populates both m.msgs (for LLM prompt rebuild) and m.blocks
// (for user display).
func TestLoadPersistedConversation_RestoresMsgsAndBlocks(t *testing.T) {
	m := newPersistTestModel(t)

	// Seed on disk directly — pretend a prior stado process wrote
	// these before exit.
	prior := []agent.Message{
		agent.Text(agent.RoleUser, "debug the auth bug"),
		agent.Text(agent.RoleAssistant, "starting with the test"),
		agent.Text(agent.RoleUser, "also look at middleware"),
	}
	for _, p := range prior {
		if err := runtime.AppendMessage(m.session.WorktreePath, p); err != nil {
			t.Fatal(err)
		}
	}

	m.LoadPersistedConversation()

	if len(m.msgs) != 3 {
		t.Errorf("m.msgs = %d, want 3", len(m.msgs))
	}
	// Last block should be the resume advisory; the three prior
	// messages sit in front of it.
	if len(m.blocks) != 4 {
		t.Fatalf("m.blocks = %d, want 4 (3 replayed + 1 resume advisory)", len(m.blocks))
	}
	if m.blocks[0].kind != "user" || !strings.Contains(m.blocks[0].body, "auth bug") {
		t.Errorf("blocks[0] = %+v", m.blocks[0])
	}
	if m.blocks[1].kind != "assistant" {
		t.Errorf("blocks[1].kind = %q, want assistant", m.blocks[1].kind)
	}
	if !strings.Contains(m.blocks[3].body, "resumed session") {
		t.Errorf("resume advisory missing from last block: %q", m.blocks[3].body)
	}
}

// TestLoadPersistedConversation_MissingFileIsNoOp covers the
// fresh-session path: no conversation.jsonl → m stays empty.
func TestLoadPersistedConversation_MissingFileIsNoOp(t *testing.T) {
	m := newPersistTestModel(t)
	m.LoadPersistedConversation()
	if len(m.msgs) != 0 {
		t.Errorf("expected 0 msgs on missing file, got %d", len(m.msgs))
	}
	if len(m.blocks) != 0 {
		t.Errorf("expected 0 blocks on missing file, got %d", len(m.blocks))
	}
}

// TestMsgsToBlocks_FlattensMultimodal: multimodal content (tool
// use + thinking + text) collapses into a single block per message
// with placeholder tags so the UI doesn't show blank turns for
// tool-heavy history.
func TestMsgsToBlocks_FlattensMultimodal(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Content: []agent.Block{
			{Thinking: &agent.ThinkingBlock{}},
			{Text: &agent.TextBlock{Text: "thought about it"}},
			{ToolUse: &agent.ToolUseBlock{Name: "grep"}},
		}},
		{Role: agent.RoleTool, Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{Content: "match found"}},
		}},
	}
	blocks := msgsToBlocks(msgs)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].kind != "assistant" {
		t.Errorf("blocks[0].kind = %q, want assistant", blocks[0].kind)
	}
	for _, want := range []string{"[thinking]", "thought about it", "[tool_use grep]"} {
		if !strings.Contains(blocks[0].body, want) {
			t.Errorf("blocks[0] body missing %q: %q", want, blocks[0].body)
		}
	}
	if blocks[1].kind != "tool" || !strings.Contains(blocks[1].body, "[tool_result]") {
		t.Errorf("blocks[1] = %+v", blocks[1])
	}
}

// TestCompaction_RewritesConversationFile: after compaction-accept,
// the on-disk file should contain only the summary message, not the
// original trail. Dual-ref tree/trace still preserves the raw turns
// elsewhere; disk-side matches the in-memory post-compaction state.
func TestCompaction_RewritesConversationFile(t *testing.T) {
	m := newPersistTestModel(t)
	m.appendUser("turn 1")
	m.appendUser("turn 2")
	m.appendUser("turn 3")

	// Seed a tree ref for compaction to land on. Empty tree is fine —
	// we're testing conversation persistence, not the commit graph.
	emptyTree, _ := m.session.BuildTreeFromDir(m.session.WorktreePath)
	if _, err := m.session.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write"}); err != nil {
		t.Fatal(err)
	}

	// Directly drive resolveCompaction without the streaming dance.
	m.state = stateCompactionPending
	m.pendingCompactionSummary = "compacted summary of turns 1-3"
	m.resolveCompaction(true)

	loaded, err := runtime.LoadConversation(m.session.WorktreePath)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	// After compaction, only the replacement-message should be on
	// disk — not the original three user turns.
	if len(loaded) >= 3 {
		t.Errorf("expected <3 msgs after compaction (got %d)", len(loaded))
	}
	// At least one message should mention the summary text.
	found := false
	for _, msg := range loaded {
		for _, b := range msg.Content {
			if b.Text != nil && strings.Contains(b.Text.Text, "compacted summary") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("compacted summary not found in persisted conversation: %+v", loaded)
	}
}
