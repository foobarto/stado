package runtime

import (
	"path/filepath"
	"strings"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// canonicalRepoPath returns a stable, comparable form of a user-repo path,
// delegating the abs + symlink-resolve to stadogit.CanonicalRepoPath (one
// shared canonicalizer, so the /home -> /var/home handling can't drift from
// the repo-id one). Returns "" for an empty input so an unset pin never
// collapses into a spurious match.
func canonicalRepoPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return stadogit.CanonicalRepoPath(p)
}

// SameUserRepo reports whether two user-repo paths refer to the same repo,
// tolerating symlinked path forms. Two empty paths are NOT equal — an unset
// pin must not match an unset current repo, so unpinned worktrees are scoped
// out rather than leaking into every listing.
func SameUserRepo(a, b string) bool {
	ca, cb := canonicalRepoPath(a), canonicalRepoPath(b)
	return ca != "" && ca == cb
}

// ListRepoWorktreeSessionIDs returns worktree-backed session IDs under
// worktreeRoot that plausibly belong to repoRoot.
//
// The worktree dir is a single global flat directory shared by every repo's
// sessions; listing it unfiltered leaks every project's sessions into every
// other project's `session list` and the TUI session manager
// (2032 cross-project entries in the operator's real env). A worktree is
// included when its user-repo pin matches repoRoot (symlink-tolerant) OR when
// it carries no pin at all — unpinned worktrees have unknown provenance, so we
// keep the historical "show it" behaviour rather than hiding a possibly-local
// stale leftover. Only a worktree pinned to a clearly different repo is
// excluded — which is exactly the leak.
func ListRepoWorktreeSessionIDs(worktreeRoot, repoRoot string) ([]string, error) {
	all, err := stadogit.ListWorktreeSessionIDs(worktreeRoot)
	if err != nil {
		return nil, err
	}
	// Canonicalize repoRoot once — it's invariant across the loop, and
	// EvalSymlinks per worktree (2000+ on a busy host) would be a needless
	// syscall storm. Only each pin is canonicalized inside the loop.
	canonRepo := canonicalRepoPath(repoRoot)
	out := make([]string, 0, len(all))
	for _, id := range all {
		pin := ReadUserRepoPin(filepath.Join(worktreeRoot, id)) // already trimmed
		if pin != "" {
			cp := canonicalRepoPath(pin)
			if cp == "" || cp != canonRepo {
				continue // pinned to a different repo → the leak we are fixing
			}
		}
		out = append(out, id)
	}
	return out, nil
}
