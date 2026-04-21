package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_AgentsMdWins: when both AGENTS.md and CLAUDE.md exist in
// the same directory, AGENTS.md is picked. This matches the
// emerging cross-vendor convention; CLAUDE.md stays supported for
// backwards compatibility with existing repos.
func TestLoad_AgentsMdWins(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "AGENTS.md"), "agents body")
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "claude body")

	r, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "agents body" {
		t.Errorf("expected AGENTS.md to win; got %q", r.Content)
	}
	if !strings.HasSuffix(r.Path, "AGENTS.md") {
		t.Errorf("expected path to end in AGENTS.md; got %q", r.Path)
	}
}

// TestLoad_ClaudeMdFallback: with only CLAUDE.md present, it loads.
func TestLoad_ClaudeMdFallback(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "legacy body")

	r, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "legacy body" {
		t.Errorf("fallback path didn't read CLAUDE.md; got %q", r.Content)
	}
}

// TestLoad_WalksUp: invocation from a subdirectory of the repo still
// finds the file. This is the common real-world case — a user
// launches stado from deep inside the tree.
func TestLoad_WalksUp(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root rules")
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Load(sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "root rules" {
		t.Errorf("walk-up didn't find root AGENTS.md; got %q", r.Content)
	}
}

// TestLoad_NoFileIsNotAnError: clean miss returns empty result with
// no error. Callers use Content=="" as the "no instructions" signal
// without special-casing an error.
func TestLoad_NoFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("miss should not error; got %v", err)
	}
	if r.Content != "" || r.Path != "" {
		t.Errorf("expected empty result, got %+v", r)
	}
}

// TestLoad_NearestWins: when a repo has AGENTS.md at multiple levels
// (monorepo), the closest one wins — tighter context beats wider.
func TestLoad_NearestWins(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root")
	sub := filepath.Join(root, "pkg", "mod")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sub, "AGENTS.md"), "module-local")

	r, err := Load(sub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Content != "module-local" {
		t.Errorf("nearest-wins failed; got %q", r.Content)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
