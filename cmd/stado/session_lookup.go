package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/go-git/go-git/v5/plumbing"
)

// completeSessionIDs is a cobra ValidArgsFunction that lists
// extant session IDs for shell tab-completion. Filters by the
// prefix the user has typed so `stado session show abc<TAB>`
// narrows. Returns IDs alongside their descriptions (cobra renders
// the description as the completion hint) when present.
//
// Returns only for first-positional-arg completion; subsequent
// args on subcommands that take just one ID get no completions
// (avoids duplicate-suggest weirdness).
func completeSessionIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) >= 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	ids, err := stadogit.ListWorktreeSessionIDs(cfg.WorktreeDir())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var completions []string
	for _, id := range ids {
		if toComplete != "" && !strings.HasPrefix(id, toComplete) {
			continue
		}
		wt, err := worktreePathForID(cfg.WorktreeDir(), id)
		if err != nil {
			continue
		}
		desc := runtime.ReadDescription(wt)
		if desc != "" {
			completions = append(completions, id+"\t"+desc)
		} else {
			completions = append(completions, id)
		}
	}
	sort.Strings(completions)
	return completions, cobra.ShellCompDirectiveNoFileComp
}

// resolveSessionID turns a user-friendly lookup string into a
// concrete session id:
//   - exact UUID match wins
//   - UUID prefix ≥ 8 chars wins when unique
//   - case-insensitive substring of session Description wins when unique
//
// Ambiguous matches return a listing error so the user can pick
// precisely. Used by `session resume` today; other commands that
// take a session id can opt in.
func resolveSessionID(cfg *config.Config, q string) (string, error) {
	if q == "" {
		return "", fmt.Errorf("empty lookup")
	}
	ids, err := stadogit.ListWorktreeSessionIDs(cfg.WorktreeDir())
	if err != nil {
		if os.IsNotExist(err) {
			// No worktree dir means no sessions exist yet. Keep the
			// message mentioning the user's query so callers that
			// surface it ("resume: no session found for X") stay
			// readable.
			return "", fmt.Errorf("no worktree dir — no sessions exist yet (looked for %q)", q)
		}
		return "", err
	}
	// Exact match short-circuit.
	for _, id := range ids {
		if id == q {
			return id, nil
		}
	}

	var prefixHits []string
	if len(q) >= 8 {
		for _, id := range ids {
			if strings.HasPrefix(id, q) {
				prefixHits = append(prefixHits, id)
			}
		}
	}
	if len(prefixHits) == 1 {
		return prefixHits[0], nil
	}
	if len(prefixHits) > 1 {
		return "", fmt.Errorf("prefix %q is ambiguous — matches: %s",
			q, strings.Join(prefixHits, ", "))
	}

	// Description substring.
	needle := strings.ToLower(q)
	var descHits []string
	for _, id := range ids {
		wt, err := worktreePathForID(cfg.WorktreeDir(), id)
		if err != nil {
			continue
		}
		desc := runtime.ReadDescription(wt)
		if desc == "" {
			continue
		}
		if strings.Contains(strings.ToLower(desc), needle) {
			descHits = append(descHits, id)
		}
	}
	if len(descHits) == 1 {
		return descHits[0], nil
	}
	if len(descHits) > 1 {
		labels := make([]string, 0, len(descHits))
		for _, id := range descHits {
			wt, err := worktreePathForID(cfg.WorktreeDir(), id)
			if err != nil {
				continue
			}
			d := runtime.ReadDescription(wt)
			labels = append(labels, fmt.Sprintf("%s (%q)", id, d))
		}
		return "", fmt.Errorf("description %q is ambiguous — matches: %s",
			q, strings.Join(labels, "; "))
	}
	return "", fmt.Errorf("no session matches %q (tried exact, prefix ≥8, description substring)", q)
}

// countCommits walks a ref's first-parent chain and returns the commit
// count. Returns 0 + nil for unset refs (fresh session) so callers can
// surface a clean "0" line.
func countCommits(sc *stadogit.Sidecar, ref plumbing.ReferenceName) (int, error) {
	head, err := sc.ResolveRef(ref)
	if err != nil {
		return 0, nil // unset ref, not an error here
	}
	repo := sc.Repo()
	count := 0
	cur := head
	for !cur.IsZero() {
		commit, err := repo.CommitObject(cur)
		if err != nil {
			return count, err
		}
		count++
		if len(commit.ParentHashes) == 0 {
			break
		}
		cur = commit.ParentHashes[0]
	}
	return count, nil
}

func findRepoRootForLand(start string) string {
	return workdirpath.FindRepoRootOrEmpty(start)
}

// refMakerSession mirrors TreeRef/TraceRef's signature within this file.
type refMakerSession func(sessionID string) plumbing.ReferenceName

// --- helpers -------------------------------------------------------------

func openSidecar(cfg *config.Config) (*stadogit.Sidecar, error) {
	cwd, _ := os.Getwd()
	userRepo := findRepoRoot(cwd)
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil {
		return nil, err
	}
	return stadogit.OpenOrInitSidecar(cfg.SidecarPath(userRepo, repoID), userRepo)
}

func worktreePathForID(root, id string) (string, error) {
	if err := stadogit.ValidateSessionID(id); err != nil {
		return "", err
	}
	wt := filepath.Join(root, id)
	return wt, nil
}

// findRepoRoot walks up from start looking for a git working tree.
// Falls back to start (canonicalised) if none found, so sessions still
// work outside repos. The working-tree predicate is shared with the
// other 5 places in the codebase via workdirpath.LooksLikeRepoRoot.
func findRepoRoot(start string) string {
	return workdirpath.FindRepoRoot(start)
}

// listSessions returns session IDs found under refs/sessions/*.
func listSessions(sc *stadogit.Sidecar) ([]string, error) {
	seen := map[string]struct{}{}
	iter, err := sc.Repo().References()
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		const prefix = "refs/sessions/"
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		rest := strings.TrimPrefix(name, prefix)
		id := strings.Split(rest, "/")[0]
		if stadogit.ValidateSessionID(id) != nil {
			return nil
		}
		seen[id] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
