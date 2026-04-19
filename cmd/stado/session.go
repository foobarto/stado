package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage stado sessions",
}

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new session",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sess, err := createSession(cfg, "")
		if err != nil {
			return err
		}
		fmt.Println(sess.ID)
		fmt.Fprintf(os.Stderr, "worktree: %s\n", sess.WorktreePath)
		return nil
	},
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sessions for the current repo",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		ids, err := listSessions(sc)
		if err != nil {
			return err
		}
		// Augment with worktree dirs — a session can exist before it has
		// committed anything, so worktree presence is the authoritative "I
		// exist" signal while refs capture progress.
		if entries, err := os.ReadDir(cfg.WorktreeDir()); err == nil {
			seen := map[string]bool{}
			for _, id := range ids {
				seen[id] = true
			}
			for _, e := range entries {
				if e.IsDir() && !seen[e.Name()] {
					ids = append(ids, e.Name())
				}
			}
			sort.Strings(ids)
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "(no sessions)")
			return nil
		}
		for _, id := range ids {
			wt := filepath.Join(cfg.WorktreeDir(), id)
			status := "detached"
			if _, err := os.Stat(wt); err == nil {
				status = "attached"
			}
			tree, _ := sc.ResolveRef(stadogit.TreeRef(id))
			fmt.Printf("%s\t%s\ttree=%s\n", id, status, short(tree))
		}
		return nil
	},
}

var sessionDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a session (refs + worktree)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		id := args[0]
		if err := sc.DeleteSessionRefs(id); err != nil {
			return fmt.Errorf("delete refs: %w", err)
		}
		if err := os.RemoveAll(filepath.Join(cfg.WorktreeDir(), id)); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}
		fmt.Fprintln(os.Stderr, "deleted", id)
		return nil
	},
}

var sessionForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Create a new session branched from an existing one's tree head",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		parent, err := createSession(cfg, args[0])
		if err != nil {
			return err
		}
		fmt.Println(parent.ID)
		fmt.Fprintf(os.Stderr, "worktree: %s\n", parent.WorktreePath)
		return nil
	},
}

var sessionAttachCmd = &cobra.Command{
	Use:   "attach <id>",
	Short: "Print worktree path of an existing session (cd $(stado session attach <id>))",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		wt := filepath.Join(cfg.WorktreeDir(), args[0])
		if _, err := os.Stat(wt); err != nil {
			return fmt.Errorf("attach: session %s has no worktree: %w", args[0], err)
		}
		fmt.Println(wt)
		return nil
	},
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Print session refs + worktree + latest commits",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		id := args[0]
		fmt.Printf("session:  %s\n", id)
		fmt.Printf("worktree: %s\n", filepath.Join(cfg.WorktreeDir(), id))
		for _, pair := range []struct {
			name string
			ref  refMakerSession
		}{
			{"tree", stadogit.TreeRef},
			{"trace", stadogit.TraceRef},
		} {
			head, err := sc.ResolveRef(pair.ref(id))
			if err != nil {
				fmt.Printf("%-6s  (unset)\n", pair.name)
				continue
			}
			fmt.Printf("%-6s  %s\n", pair.name, head.String()[:12])
		}
		return nil
	},
}

var sessionLandCmd = &cobra.Command{
	Use:   "land <id> <branch>",
	Short: "Push the session's tree head to the user repo as <branch>",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, branch := args[0], args[1]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		head, err := sc.ResolveRef(stadogit.TreeRef(id))
		if err != nil {
			return fmt.Errorf("land: session %s has no tree ref: %w", id, err)
		}

		// Locate the user repo.
		cwd, _ := os.Getwd()
		userRepoRoot := findRepoRootForLand(cwd)
		if userRepoRoot == "" {
			return errors.New("land: current directory is not inside a git repo")
		}
		userRepo, err := gogit.PlainOpen(userRepoRoot)
		if err != nil {
			return fmt.Errorf("land: open user repo: %w", err)
		}

		// The sidecar alternates means objects for `head` are reachable in the
		// user repo's object store too. We only need to create the ref.
		refName := plumbing.ReferenceName("refs/heads/" + branch)
		if err := userRepo.Storer.SetReference(plumbing.NewHashReference(refName, head)); err != nil {
			return fmt.Errorf("land: set %s: %w", refName, err)
		}
		fmt.Fprintf(os.Stderr, "landed %s → %s @ %s\n", id, refName, head.String()[:12])
		return nil
	},
}

func findRepoRootForLand(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// refMakerSession mirrors TreeRef/TraceRef's signature within this file.
type refMakerSession func(sessionID string) plumbing.ReferenceName

func init() {
	sessionCmd.AddCommand(
		sessionNewCmd, sessionListCmd, sessionDeleteCmd, sessionForkCmd,
		sessionAttachCmd, sessionShowCmd, sessionLandCmd, sessionRevertCmd,
	)
	rootCmd.AddCommand(sessionCmd)
}

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

// createSession makes a new session, optionally branched from parentID.
// Forked sessions have their worktree materialised to the parent's tree so
// the child starts with the same files the parent had.
func createSession(cfg *config.Config, parentID string) (*stadogit.Session, error) {
	sc, err := openSidecar(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
		return nil, err
	}

	var parentTreeCommit plumbing.Hash
	if parentID != "" {
		head, err := sc.ResolveRef(stadogit.TreeRef(parentID))
		switch {
		case err == nil:
			parentTreeCommit = head
		case errors.Is(err, plumbing.ErrReferenceNotFound):
			// Parent hasn't committed anything yet — fork is equivalent to a
			// fresh session. Not an error.
			fmt.Fprintf(os.Stderr, "fork: parent %s has no tree ref yet; creating empty child\n", parentID)
		default:
			return nil, fmt.Errorf("fork: resolve parent: %w", err)
		}
	}

	id := uuid.New().String()
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, parentTreeCommit)
	if err != nil {
		return nil, err
	}

	// Materialise the parent's tree into the child's worktree. Empty parent
	// tree = clean worktree.
	if !parentTreeCommit.IsZero() {
		treeHash, err := sess.TreeFromCommit(parentTreeCommit)
		if err != nil {
			return nil, fmt.Errorf("fork: resolve tree: %w", err)
		}
		if err := sess.MaterializeTreeToDir(treeHash, sess.WorktreePath); err != nil {
			return nil, fmt.Errorf("fork: materialise worktree: %w", err)
		}
	}
	return sess, nil
}

var sessionRevertCmd = &cobra.Command{
	Use:   "revert <id> <commit-or-turn>",
	Short: "Reset a session's worktree to an earlier commit on a new child session",
	Long: "Create a new session whose tree ref points at the given historical commit\n" +
		"(or turns/<N> tag) from <id>'s history, and materialise the worktree to\n" +
		"match. The parent session is untouched — revert is non-destructive and\n" +
		"lives on a fresh session ID.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		srcID, target := args[0], args[1]
		commitHash, err := resolveRevertTarget(sc, srcID, target)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(cfg.WorktreeDir(), 0o755); err != nil {
			return err
		}
		newID := uuid.New().String()
		sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), newID, commitHash)
		if err != nil {
			return err
		}
		treeHash, err := sess.TreeFromCommit(commitHash)
		if err != nil {
			return fmt.Errorf("revert: read tree from commit: %w", err)
		}
		if err := sess.MaterializeTreeReplacing(treeHash, sess.WorktreePath); err != nil {
			return fmt.Errorf("revert: materialise: %w", err)
		}
		fmt.Println(newID)
		fmt.Fprintf(os.Stderr, "reverted %s@%s → new session %s (worktree %s)\n",
			srcID, commitHash.String()[:12], newID, sess.WorktreePath)
		return nil
	},
}

// resolveRevertTarget accepts either a commit prefix (looked up via a linear
// walk of src's tree ref) or "turns/<N>" which resolves via the turn-tag ref.
func resolveRevertTarget(sc *stadogit.Sidecar, srcID, target string) (plumbing.Hash, error) {
	if strings.HasPrefix(target, "turns/") {
		ref := plumbing.ReferenceName("refs/sessions/" + srcID + "/" + target)
		h, err := sc.ResolveRef(ref)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("revert: tag %s not found: %w", target, err)
		}
		return h, nil
	}
	// Treat as raw hash or prefix. v1: only accept full 40-char hashes.
	if len(target) < 40 {
		return plumbing.ZeroHash, fmt.Errorf("revert: pass a full 40-char commit sha or turns/<N>")
	}
	return plumbing.NewHash(target), nil
}

// findRepoRoot walks up from start looking for a .git dir. Falls back to the
// starting cwd if none found (so sessions still work outside repos).
func findRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
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

func short(h plumbing.Hash) string {
	if h.IsZero() {
		return "-"
	}
	return h.String()[:7]
}

// Silence "gogit imported but not used" — cobra command file uses it for
// error typing in case we later want to branch on ErrRepositoryNotExists.
var _ = gogit.ErrRepositoryNotExists
var _ = errors.New
