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

// searchEnv creates two sessions with seeded conversations so cross-
// session search has something to find. Returns cleanup.
func searchEnv(t *testing.T) (*config.Config, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, err := openSidecar(cfg)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	sess1, _ := stadogit.CreateSession(sc, cfg.WorktreeDir(), "session-alpha", plumbing.ZeroHash)
	sess2, _ := stadogit.CreateSession(sc, cfg.WorktreeDir(), "session-beta", plumbing.ZeroHash)

	_ = runtime.AppendMessage(sess1.WorktreePath, agent.Text(agent.RoleUser, "how do I refactor this React component?"))
	_ = runtime.AppendMessage(sess1.WorktreePath, agent.Text(agent.RoleAssistant, "Extract the useEffect hook into a custom hook."))
	_ = runtime.AppendMessage(sess2.WorktreePath, agent.Text(agent.RoleUser, "write a Go program that greets me"))
	_ = runtime.AppendMessage(sess2.WorktreePath, agent.Text(agent.RoleAssistant, "package main\nfunc main() { fmt.Println(\"hello\") }"))
	return cfg, restore
}

// captureSearchStdout wraps the session_show_test.go captureStdout
// helper for the search-tests' func() error signature (RunE returns
// an error, so tests need to propagate).
func captureSearchStdout(t *testing.T, fn func() error) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := fn(); err != nil {
			t.Fatalf("fn: %v", err)
		}
	})
}

// TestSessionSearch_SubstringFindsBothSessions: default substring
// matcher is case-insensitive; "hook" finds the React session's
// assistant line (capital H doesn't matter) but not the Go one.
func TestSessionSearch_SubstringFindsBothSessions(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = false
	searchSession = ""
	searchMax = 0

	out := captureSearchStdout(t, func() error {
		return sessionSearchCmd.RunE(sessionSearchCmd, []string{"hook"})
	})

	if !strings.Contains(out, "session:session-alpha") {
		t.Errorf("expected alpha session match: %q", out)
	}
	if strings.Contains(out, "session:session-beta") {
		t.Errorf("beta session should NOT match 'hook': %q", out)
	}
}

// TestSessionSearch_CaseInsensitive: query "REACT" (uppercase) finds
// "React" in the conversation.
func TestSessionSearch_CaseInsensitive(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = false
	searchSession = ""
	out := captureSearchStdout(t, func() error {
		return sessionSearchCmd.RunE(sessionSearchCmd, []string{"REACT"})
	})

	if !strings.Contains(out, "session-alpha") {
		t.Errorf("case-insensitive: expected match, got: %q", out)
	}
}

// TestSessionSearch_RegexMode: --regex + "h[eo]+ll?o" matches Go's
// "hello"; --regex + "h[eo]+ll?o" NOT matching "hook" is the
// distinguishing case.
func TestSessionSearch_RegexMode(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = true
	searchSession = ""
	defer func() { searchRegex = false }()

	out := captureSearchStdout(t, func() error {
		return sessionSearchCmd.RunE(sessionSearchCmd, []string{`hel+o`})
	})

	if !strings.Contains(out, "session-beta") {
		t.Errorf("regex should match 'hello' in beta: %q", out)
	}
}

// TestSessionSearch_SessionFilter: --session <id> scopes search.
func TestSessionSearch_SessionFilter(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = false
	searchSession = "session-beta"
	defer func() { searchSession = "" }()

	out := captureSearchStdout(t, func() error {
		return sessionSearchCmd.RunE(sessionSearchCmd, []string{"main"})
	})

	if !strings.Contains(out, "session-beta") {
		t.Errorf("expected beta match, got: %q", out)
	}
	if strings.Contains(out, "session-alpha") {
		t.Errorf("--session=beta leaked alpha into results: %q", out)
	}
}

// TestSessionSearch_MaxHonored: --max caps output.
func TestSessionSearch_MaxHonored(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = false
	searchSession = ""
	searchMax = 1
	defer func() { searchMax = 0 }()

	out := captureSearchStdout(t, func() error {
		// 'the' appears in both sessions multiple times... but our
		// seeded data doesn't have it. Use 'e' which does.
		return sessionSearchCmd.RunE(sessionSearchCmd, []string{"e"})
	})

	// Count "session:" occurrences — one per match line.
	got := strings.Count(out, "session:")
	if got != 1 {
		t.Errorf("--max=1 should produce exactly 1 match line; got %d\noutput:\n%s",
			got, out)
	}
}

// TestSessionSearch_NoMatches: query that hits nothing prints the
// "(no matches)" sentinel via stderr; exit code 0.
func TestSessionSearch_NoMatches(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = false
	searchSession = ""
	err := sessionSearchCmd.RunE(sessionSearchCmd, []string{"zzzzzzzzz-not-present"})
	if err != nil {
		t.Errorf("no-match path should exit 0, got: %v", err)
	}
}

// TestSessionSearch_RegexCompileError: bad regex surfaces cleanly.
func TestSessionSearch_RegexCompileError(t *testing.T) {
	_, restore := searchEnv(t)
	defer restore()

	searchRegex = true
	defer func() { searchRegex = false }()
	err := sessionSearchCmd.RunE(sessionSearchCmd, []string{"*broken"})
	if err == nil {
		t.Fatal("bad regex should error")
	}
	if !strings.Contains(err.Error(), "bad regex") {
		t.Errorf("error should mention bad regex: %v", err)
	}
}

func TestSessionSearch_StripsTerminalControlChars(t *testing.T) {
	cfg, restore := searchEnv(t)
	defer restore()

	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), "session-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.AppendMessage(sess.WorktreePath, agent.Text(agent.RoleAssistant, "bad\x1bexcerpt")); err != nil {
		t.Fatal(err)
	}

	searchRegex = false
	searchSession = "session-alpha"
	defer func() { searchSession = "" }()

	out := captureSearchStdout(t, func() error {
		return sessionSearchCmd.RunE(sessionSearchCmd, []string{"excerpt"})
	})
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("control chars leaked into search output: %q", out)
	}
	if !strings.Contains(out, "badexcerpt") {
		t.Fatalf("sanitized excerpt missing: %q", out)
	}
}
