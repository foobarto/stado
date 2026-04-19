package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// TestSummariseSession_DetachedNeverCommitted — an ID the sidecar
// knows nothing about. Every numeric field stays zero, LastActive
// returns "never", status is detached.
func TestSummariseSession_DetachedNeverCommitted(t *testing.T) {
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)
	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, base)
	if err != nil {
		t.Fatal(err)
	}

	got := SummariseSession(worktreeRoot, sc, "phantom-id")
	if got.Status != "detached" {
		t.Errorf("Status = %q, want detached", got.Status)
	}
	if got.Turns != 0 || got.Msgs != 0 || got.Compactions != 0 {
		t.Errorf("expected zero counts, got %+v", got)
	}
	if got.LastActiveFormatted() != "never" {
		t.Errorf("LastActiveFormatted = %q, want never", got.LastActiveFormatted())
	}
}

// TestSummariseSession_AttachedRichMetadata — the happy path. Seed a
// session with turn tags, a compaction, and a conversation file, then
// assert every field of SessionSummary is populated.
func TestSummariseSession_AttachedRichMetadata(t *testing.T) {
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)
	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, base)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, worktreeRoot, "s-rich", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	// Two turn tags + one tree commit + one compaction.
	emptyTree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	if _, err := sess.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}
	if err := sess.NextTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.NextTurn(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sess.CommitCompaction(stadogit.CompactionMeta{
		Title: "one", FromTurn: 0, ToTurn: 2, TurnsTotal: 2, ByAuthor: "tester",
	}); err != nil {
		t.Fatal(err)
	}
	// Two persisted conversation messages.
	_ = AppendMessage(sess.WorktreePath, agent.Text(agent.RoleUser, "one"))
	_ = AppendMessage(sess.WorktreePath, agent.Text(agent.RoleAssistant, "two"))

	r := SummariseSession(worktreeRoot, sc, sess.ID)
	if r.Status != "attached" {
		t.Errorf("Status = %q, want attached", r.Status)
	}
	if r.Turns != 2 {
		t.Errorf("Turns = %d, want 2", r.Turns)
	}
	if r.Compactions != 1 {
		t.Errorf("Compactions = %d, want 1", r.Compactions)
	}
	if r.Msgs != 2 {
		t.Errorf("Msgs = %d, want 2", r.Msgs)
	}
	if r.LastActive.IsZero() {
		t.Error("LastActive should be populated from the latest turn tag")
	}
	if !strings.Contains(r.LastActiveFormatted(), "UTC") {
		t.Errorf("formatted time missing UTC marker: %q", r.LastActiveFormatted())
	}
}

// TestLastActiveFormatted_ZeroReturnsNever pins the "never"
// placeholder so session-list rendering stays stable.
func TestLastActiveFormatted_ZeroReturnsNever(t *testing.T) {
	var s SessionSummary
	if got := s.LastActiveFormatted(); got != "never" {
		t.Errorf("zero LastActive → %q, want never", got)
	}
}
