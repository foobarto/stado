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
	// No .stado-pid file was written (attachSessionScaffolding wasn't
	// called in this test), so the worktree-exists path lands on
	// "idle", not "live".
	if r.Status != "idle" {
		t.Errorf("Status = %q, want idle", r.Status)
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

// TestSummariseSession_LivePIDPromotesToLive — drop a .stado-pid file
// containing the current test process's pid (which is definitely
// alive), and Status should resolve to "live" with PID populated. The
// dogfood report #5 fix: "attached" used to mean "worktree exists on
// disk" regardless of whether a process was using it.
func TestSummariseSession_LivePIDPromotesToLive(t *testing.T) {
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)
	sc, _ := stadogit.OpenOrInitSidecar(sidecarPath, base)
	sess, err := stadogit.CreateSession(sc, worktreeRoot, "s-live", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(sess.WorktreePath, ".stado-pid")
	pid := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	r := SummariseSession(worktreeRoot, sc, sess.ID)
	if r.Status != "live" {
		t.Errorf("Status = %q, want live", r.Status)
	}
	if r.PID != pid {
		t.Errorf("PID = %d, want %d", r.PID, pid)
	}
}

func TestReadDescription_StripsTerminalControlChars(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDescription(dir, "hello\x1b world"); err != nil {
		t.Fatal(err)
	}
	got := ReadDescription(dir)
	if got != "hello world" {
		t.Fatalf("unexpected sanitized description: %q", got)
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("control chars leaked from description: %q", got)
	}
}

func TestWriteDescriptionRejectsSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".stado"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, DescriptionFile)); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := WriteDescription(dir, "escaped"); err == nil {
		t.Fatal("WriteDescription succeeded through a symlink escape")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside\n" {
		t.Fatalf("outside file was modified: %q", got)
	}
}

func TestReadUserRepoPinRejectsStadoDirSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "user-repo"), []byte("/outside/repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(dir, ".stado")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if got := ReadUserRepoPin(dir); got != "" {
		t.Fatalf("ReadUserRepoPin followed .stado symlink escape: %q", got)
	}
}

func TestWriteUserRepoPinRejectsStadoDirSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	dir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(dir, ".stado")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := WriteUserRepoPin(dir, "/outside/repo"); err == nil {
		t.Fatal("WriteUserRepoPin succeeded through a .stado symlink escape")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "user-repo")); !os.IsNotExist(err) {
		t.Fatalf("outside user-repo should not exist, got %v", err)
	}
}

// TestSummariseSession_StalePIDFallsBackToIdle — a .stado-pid file
// pointing at a non-existent pid must NOT be read as live. 2147483640
// is a very-high pid unlikely to exist; os.FindProcess will "succeed"
// but signal(0) fails → idle.
func TestSummariseSession_StalePIDFallsBackToIdle(t *testing.T) {
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)
	sc, _ := stadogit.OpenOrInitSidecar(sidecarPath, base)
	sess, _ := stadogit.CreateSession(sc, worktreeRoot, "s-stale", plumbing.ZeroHash)
	pidPath := filepath.Join(sess.WorktreePath, ".stado-pid")
	_ = os.WriteFile(pidPath, []byte("2147483640"), 0o644)
	r := SummariseSession(worktreeRoot, sc, sess.ID)
	if r.Status != "idle" {
		t.Errorf("Status = %q, want idle (stale pid)", r.Status)
	}
	if r.PID != 0 {
		t.Errorf("PID = %d, want 0 when not live", r.PID)
	}
}

// itoa keeps the test self-contained without reaching for strconv.
func itoa(i int) string {
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 || pos == len(buf) {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
