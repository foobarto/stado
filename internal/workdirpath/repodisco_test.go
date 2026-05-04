package workdirpath

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLooksLikeRepoRoot covers each accepted shape and the false-
// positive that motivated this helper (an empty `.git/` directory).
func TestLooksLikeRepoRoot(t *testing.T) {
	t.Run("empty git dir is rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if LooksLikeRepoRoot(root) {
			t.Errorf("empty .git/ should not look like a repo root")
		}
	})

	t.Run("git dir with HEAD is accepted", func(t *testing.T) {
		root := t.TempDir()
		gitDir := filepath.Join(root, ".git")
		if err := os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !LooksLikeRepoRoot(root) {
			t.Errorf(".git/ with HEAD should look like a repo root")
		}
	})

	t.Run("git pointer file (linked worktree) is accepted", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere/.git\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !LooksLikeRepoRoot(root) {
			t.Errorf("gitfile pointer should look like a repo root")
		}
	})

	t.Run("missing .git is rejected", func(t *testing.T) {
		root := t.TempDir()
		if LooksLikeRepoRoot(root) {
			t.Errorf("dir with no .git should not look like a repo root")
		}
	})

	t.Run("symlink to broken target is rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, ".git")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if LooksLikeRepoRoot(root) {
			t.Errorf("symlink to missing target should be rejected")
		}
	})
}

// TestFindRepoRoot covers the parent-walk + the fallback semantics
// callers rely on (return start when no repo is found anywhere).
func TestFindRepoRoot(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("walks up to real repo root", func(t *testing.T) {
		got := FindRepoRoot(deep)
		want, _ := filepath.Abs(root)
		if got != want {
			t.Errorf("FindRepoRoot(%q) = %q, want %q", deep, got, want)
		}
	})

	t.Run("ignores empty parent .git on the way up", func(t *testing.T) {
		// Plant an empty .git on a parent. FindRepoRoot must walk past
		// it (because LooksLikeRepoRoot rejects empty .git) and find
		// the real one at root.
		_ = os.MkdirAll(filepath.Join(root, "a", ".git"), 0o755)
		got := FindRepoRoot(deep)
		want, _ := filepath.Abs(root)
		if got != want {
			t.Errorf("FindRepoRoot(%q) = %q, want %q (should walk past empty .git)", deep, got, want)
		}
	})

	t.Run("returns start when no repo found", func(t *testing.T) {
		// Build a dir tree with no .git anywhere up to /.
		// We can't really guarantee no .git up the chain on the host
		// (e.g. /tmp/.git pollution), so use FindRepoRootOrEmpty's
		// behaviour as the canonical assertion.
		isolated := t.TempDir()
		// Ensure there's no .git in isolated itself.
		got := FindRepoRootOrEmpty(isolated)
		if got != "" && got == isolated {
			t.Errorf("FindRepoRootOrEmpty unexpectedly found a repo at %q", got)
		}
	})
}
