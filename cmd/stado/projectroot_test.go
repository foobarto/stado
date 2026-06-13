package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectRoot_IgnoresHeadlessGitDir guards EP-0027: the daemon's repo-root
// walk used a bare `.git` stat with no HEAD check, so a stray HEAD-less `.git`
// directory was wrongly accepted as a repo root (and produced a bogus repo-id).
// It now delegates to workdirpath.LooksLikeRepoRoot, which requires a HEAD.
func TestProjectRoot_IgnoresHeadlessGitDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	child := filepath.Join(repo, "sub", "deep")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil { // .git dir, NO HEAD yet
		t.Fatal(err)
	}

	// Before EP-0027 the naive `.git` stat accepted this as the repo root.
	if got := projectRoot(child); got == repo {
		t.Errorf("projectRoot accepted a HEAD-less .git dir as a repo root: %q", got)
	}

	// With a real HEAD it is a genuine repo root and is discovered.
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectRoot(child); got != repo {
		t.Errorf("projectRoot should find the repo root once HEAD exists: got %q want %q", got, repo)
	}
}
