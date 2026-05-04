package workdirpath

import (
	"os"
	"path/filepath"
)

// LooksLikeRepoRoot reports whether dir appears to be the root of a
// git working tree.
//
// A bare `os.Stat(dir+"/.git")` is not enough: stray empty `.git`
// directories (test fixtures, leftovers from a botched
// `rm -rf project`, mount points) produce false positives. When a
// caller walking up the parent chain accepts those, the result is a
// repo-root path that points somewhere unrelated to the user's actual
// project — which then breaks downstream pinning (session user-repo
// pin, GC scoping, lesson-document target paths).
//
// We accept dir as a repo root when one of the following holds:
//
//  1. `<dir>/.git` is a regular file (or symlink to one). git uses a
//     gitfile pointer for linked worktrees and submodule checkouts;
//     its mere presence is intentional.
//  2. `<dir>/.git` is a directory containing a `HEAD` entry. Every
//     real git tree (whether bare-as-tree or normal) has HEAD; an
//     empty `.git/` directory does not.
//
// We don't enumerate every config file git would accept (objects/,
// refs/, config) because HEAD is the cheapest, most universally-present
// marker, and tightening past it doesn't fix any known false-positive
// pattern in this repo.
func LooksLikeRepoRoot(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return false
	}
	// Symlinks: resolve once. A symlink to a missing target → not a repo.
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(gitPath)
		if err != nil {
			return false
		}
	}
	if !info.IsDir() {
		// gitfile pointer (e.g. linked worktree, submodule) — accept.
		return true
	}
	if _, err := os.Stat(filepath.Join(gitPath, "HEAD")); err != nil {
		return false
	}
	return true
}

// FindRepoRoot walks up from start looking for a repository root.
// Returns the discovered root, or `start` (canonicalised when possible)
// when no repository is found anywhere up the chain — matching the
// historical "fall back to cwd" semantics callers rely on.
//
// Use FindRepoRootOrEmpty when the caller wants to distinguish
// "no repo found" from "found at start".
func FindRepoRoot(start string) string {
	if r := findRepoRootOrEmpty(start); r != "" {
		return r
	}
	if abs, err := filepath.Abs(start); err == nil {
		return abs
	}
	return start
}

// FindRepoRootOrEmpty walks up from start looking for a repository
// root. Returns "" when no repository is found.
func FindRepoRootOrEmpty(start string) string {
	return findRepoRootOrEmpty(start)
}

func findRepoRootOrEmpty(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		dir = start
	}
	for {
		if LooksLikeRepoRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
