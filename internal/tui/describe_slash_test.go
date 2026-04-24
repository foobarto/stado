package tui

// Direct-Update tests for the /describe slash command (task #106).
// Parallel to the CLI session_describe_test.go but drives through
// the TUI's handleSlash path.

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

// describeSlashModel wires a Model with a real live stadogit Session
// so handleDescribeSlash's m.session.WorktreePath is valid for read/
// write via runtime.ReadDescription / WriteDescription.
func describeSlashModel(t *testing.T) *Model {
	t.Helper()
	base := t.TempDir()
	sidecarPath := filepath.Join(base, "sessions.git")
	worktreeRoot := filepath.Join(base, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)

	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, base)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, worktreeRoot, "describe-slash", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	rnd, _ := render.New(theme.Default())
	m := NewModel(base, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.session = sess
	m.width, m.height = 120, 30
	return m
}

// TestUAT_DescribeSlashWritesDescription: /describe <text> writes to
// .stado/description via runtime.WriteDescription.
func TestUAT_DescribeSlashWritesDescription(t *testing.T) {
	m := describeSlashModel(t)
	priorBlocks := len(m.blocks)

	m.handleDescribeSlash([]string{"/describe", "react", "refactor", "session"})

	wt := m.session.WorktreePath
	got := runtime.ReadDescription(wt)
	if got != "react refactor session" {
		t.Errorf("description = %q, want 'react refactor session'", got)
	}
	// System block confirms.
	if len(m.blocks) != priorBlocks+1 {
		t.Fatalf("expected 1 new block; got %d", len(m.blocks)-priorBlocks)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "description set") {
		t.Errorf("confirmation block wrong: %+v", last)
	}
}

// TestUAT_DescribeSlashClears: /describe --clear wipes the label.
func TestUAT_DescribeSlashClears(t *testing.T) {
	m := describeSlashModel(t)
	_ = runtime.WriteDescription(m.session.WorktreePath, "initial")

	m.handleDescribeSlash([]string{"/describe", "--clear"})

	if got := runtime.ReadDescription(m.session.WorktreePath); got != "" {
		t.Errorf("after --clear: %q, want empty", got)
	}
}

// TestUAT_DescribeSlashReadOnly: no args → prints current label.
func TestUAT_DescribeSlashReadOnly(t *testing.T) {
	m := describeSlashModel(t)
	_ = runtime.WriteDescription(m.session.WorktreePath, "current-label")
	priorBlocks := len(m.blocks)

	m.handleDescribeSlash([]string{"/describe"})

	if len(m.blocks) != priorBlocks+1 {
		t.Fatalf("expected 1 new block")
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.body, "current-label") {
		t.Errorf("read-only block missing label: %q", last.body)
	}
}

// TestUAT_DescribeSlashNoLiveSession: nil session → friendly error.
func TestUAT_DescribeSlashNoLiveSession(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.session = nil
	m.width, m.height = 120, 30

	m.handleDescribeSlash([]string{"/describe", "anything"})
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.body, "no live session") {
		t.Errorf("expected 'no live session' message: %q", last.body)
	}
}

// TestUAT_SidebarShowsSessionDescription: after setting a label,
// the sidebar's rendered output must contain it (directly under
// the title).
func TestUAT_SidebarShowsSessionDescription(t *testing.T) {
	m := describeSlashModel(t)
	_ = runtime.WriteDescription(m.session.WorktreePath, "sidebar test label")

	got := m.renderSidebar(40)
	if !strings.Contains(got, "sidebar test label") {
		t.Errorf("sidebar missing description: %q", got)
	}
}

// TestUAT_SidebarHidesEmptyDescription: no label still renders the
// sidebar header cleanly instead of showing an empty placeholder row.
func TestUAT_SidebarHidesEmptyDescription(t *testing.T) {
	m := describeSlashModel(t)
	_ = runtime.WriteDescription(m.session.WorktreePath, "")

	got := m.renderSidebar(40)
	if !strings.Contains(got, "stado") {
		t.Errorf("sidebar should still render title: %q", got)
	}
}

func TestUAT_AutoTitleFirstPrompt(t *testing.T) {
	m := describeSlashModel(t)

	m.appendUser("  fix the flaky tmux harness after landing view  ")

	got := runtime.ReadDescription(m.session.WorktreePath)
	if got != "fix the flaky tmux harness after landing view" {
		t.Fatalf("auto title = %q", got)
	}
}

func TestUAT_AutoTitleDoesNotOverwriteManualDescription(t *testing.T) {
	m := describeSlashModel(t)
	_ = runtime.WriteDescription(m.session.WorktreePath, "manual label")

	m.appendUser("replace me")

	got := runtime.ReadDescription(m.session.WorktreePath)
	if got != "manual label" {
		t.Fatalf("description overwritten: %q", got)
	}
}

func TestUAT_AutoTitleSkipsResumedConversation(t *testing.T) {
	m := describeSlashModel(t)
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, "prior prompt"))

	m.appendUser("new prompt in resumed session")

	got := runtime.ReadDescription(m.session.WorktreePath)
	if got != "" {
		t.Fatalf("resumed conversation got auto title: %q", got)
	}
}

func TestAutoSessionTitleTruncates(t *testing.T) {
	got := autoSessionTitle("one two three four five six seven eight nine ten eleven twelve")
	if len([]rune(got)) > autoSessionTitleMaxRunes+3 {
		t.Fatalf("title too long: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated title missing suffix: %q", got)
	}
}

func TestAutoSessionTitleStripsControlChars(t *testing.T) {
	got := autoSessionTitle("hello\x1b[31m world")
	if got != "hello[31m world" {
		t.Fatalf("control chars not stripped: %q", got)
	}
}
