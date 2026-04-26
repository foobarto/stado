package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// exportEnv builds a clean-room config + session and seeds the
// .stado/conversation.jsonl with a few messages. Returns session
// id + cleanup.
func exportEnv(t *testing.T) (sessionID string, cfg *config.Config, cleanup func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)

	cfg, _ = config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, err := openSidecar(cfg)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	id := "export-test"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	// Seed three messages: user, assistant with text + tool_use, tool result.
	_ = runtime.AppendMessage(sess.WorktreePath, agent.Text(agent.RoleUser, "find main.go"))
	_ = runtime.AppendMessage(sess.WorktreePath, agent.Message{
		Role: agent.RoleAssistant,
		Content: []agent.Block{
			{Text: &agent.TextBlock{Text: "I'll search for it."}},
			{ToolUse: &agent.ToolUseBlock{ID: "t-1", Name: "grep", Input: []byte(`{"pattern":"main","path":"."}`)}},
		},
	})
	_ = runtime.AppendMessage(sess.WorktreePath, agent.Message{
		Role: agent.RoleTool,
		Content: []agent.Block{
			{ToolResult: &agent.ToolResultBlock{ToolUseID: "t-1", Content: "main.go:1", IsError: false}},
		},
	})
	return id, cfg, restore
}

// TestSessionExport_MarkdownFormat: default output is markdown with
// role headers, the user prompt, a fenced tool-call JSON, and a
// fenced tool-result body.
func TestSessionExport_MarkdownFormat(t *testing.T) {
	id, _, restore := exportEnv(t)
	defer restore()

	out := t.TempDir()
	exportFormat = "md"
	exportOutput = filepath.Join(out, "out.md")
	defer func() { exportFormat = "md"; exportOutput = "" }()

	if err := sessionExportCmd.RunE(sessionExportCmd, []string{id}); err != nil {
		t.Fatalf("export: %v", err)
	}
	body, err := os.ReadFile(exportOutput)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		"# Session export-test",
		"## 1. User",
		"find main.go",
		"## 2. Assistant",
		"I'll search for it.",
		"**Tool call:** `grep`",
		"\"pattern\":\"main\"",
		"## 3. Tool result",
		"**Tool result**",
		"main.go:1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("markdown output missing %q", want)
		}
	}
}

// TestSessionExport_JSONLFormat: jsonl output is the raw on-disk
// file, verbatim. Same bytes as reading the file directly.
func TestSessionExport_JSONLFormat(t *testing.T) {
	id, cfg, restore := exportEnv(t)
	defer restore()

	wt := filepath.Join(cfg.WorktreeDir(), id)
	raw, err := os.ReadFile(filepath.Join(wt, runtime.ConversationFile))
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	exportFormat = "jsonl"
	exportOutput = filepath.Join(out, "out.jsonl")
	defer func() { exportFormat = "md"; exportOutput = "" }()

	if err := sessionExportCmd.RunE(sessionExportCmd, []string{id}); err != nil {
		t.Fatalf("export: %v", err)
	}
	got, err := os.ReadFile(exportOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Errorf("jsonl export differs from source\nwant:\n%s\ngot:\n%s", raw, got)
	}
}

func TestSessionExportRejectsOutputSymlink(t *testing.T) {
	id, _, restore := exportEnv(t)
	defer restore()

	out := t.TempDir()
	decoy := filepath.Join(out, "decoy.md")
	if err := os.WriteFile(decoy, []byte("do not replace"), 0o644); err != nil {
		t.Fatal(err)
	}
	exportFormat = "md"
	exportOutput = filepath.Join(out, "out.md")
	defer func() { exportFormat = "md"; exportOutput = "" }()
	if err := os.Symlink("decoy.md", exportOutput); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := sessionExportCmd.RunE(sessionExportCmd, []string{id}); err == nil {
		t.Fatal("session export should reject symlinked output path")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not replace" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestSessionExportRejectsOutputParentSymlink(t *testing.T) {
	id, _, restore := exportEnv(t)
	defer restore()

	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("outside", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	exportFormat = "md"
	exportOutput = filepath.Join(link, "out.md")
	defer func() { exportFormat = "md"; exportOutput = "" }()

	if err := sessionExportCmd.RunE(sessionExportCmd, []string{id}); err == nil {
		t.Fatal("session export should reject symlinked output parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "out.md")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified, stat err = %v", err)
	}
}

// TestSessionExport_StdoutWhenNoOutput: empty --output (or "-")
// writes to stdout rather than a file.
func TestSessionExport_StdoutWhenNoOutput(t *testing.T) {
	id, _, restore := exportEnv(t)
	defer restore()

	// Capture stdout.
	oldOut := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = oldOut }()

	exportFormat = "md"
	exportOutput = "" // stdout
	defer func() { exportFormat = "md" }()

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		done <- buf[:n]
	}()

	if err := sessionExportCmd.RunE(sessionExportCmd, []string{id}); err != nil {
		t.Fatalf("export: %v", err)
	}
	_ = w.Close()
	got := <-done
	if !strings.Contains(string(got), "# Session export-test") {
		t.Errorf("stdout export missing header: %q", got)
	}
}

// TestSessionExport_UnknownSession: an id that doesn't exist on
// disk errors out with a clear message.
func TestSessionExport_UnknownSession(t *testing.T) {
	_, _, restore := exportEnv(t)
	defer restore()

	exportFormat = "md"
	exportOutput = ""
	err := sessionExportCmd.RunE(sessionExportCmd, []string{"does-not-exist"})
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say not found: %v", err)
	}
}

// TestSessionExport_EmptyConversation: a session with no persisted
// messages errors with an explanation rather than producing an
// empty file.
func TestSessionExport_EmptyConversation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, _ := openSidecar(cfg)
	id := "empty-session"
	if _, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash); err != nil {
		t.Fatal(err)
	}

	exportFormat = "md"
	exportOutput = ""
	err := sessionExportCmd.RunE(sessionExportCmd, []string{id})
	if err == nil {
		t.Fatal("expected error for empty conversation")
	}
	if !strings.Contains(err.Error(), "no persisted conversation") {
		t.Errorf("error should explain empty conversation: %v", err)
	}
}

// TestSessionExport_UnknownFormat: bad --format fails fast.
func TestSessionExport_UnknownFormat(t *testing.T) {
	id, _, restore := exportEnv(t)
	defer restore()

	exportFormat = "pdf"
	exportOutput = ""
	defer func() { exportFormat = "md" }()

	err := sessionExportCmd.RunE(sessionExportCmd, []string{id})
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if !strings.Contains(err.Error(), "unknown --format") {
		t.Errorf("error should mention unknown format: %v", err)
	}
}
