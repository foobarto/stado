package main

import (
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// openPersistedSession opens a session by worktree ID rather than by the
// caller's cwd. When the worktree has a pinned user-repo path, that pin wins
// so session-scoped CLI flows keep targeting the original sidecar even when
// invoked from elsewhere.
func openPersistedSession(cfg *config.Config, id string) (*stadogit.Sidecar, *stadogit.Session, error) {
	wt, err := worktreePathForID(cfg.WorktreeDir(), id)
	if err != nil {
		return nil, nil, err
	}

	if runtime.ReadUserRepoPin(wt) == "" {
		cwd, _ := os.Getwd()
		userRepo := findRepoRoot(cwd)
		dir := filepath.Join(wt, ".stado")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "user-repo"), []byte(userRepo+"\n"), 0o644)
	}

	sess, err := runtime.OpenSessionByID(cfg, wt, id)
	if err != nil {
		return nil, nil, err
	}
	return sess.Sidecar, sess, nil
}

func lastPersistedTurnRef(sc *stadogit.Sidecar, id string) string {
	if sc == nil {
		return ""
	}
	turns, err := sc.ListTurnRefs(id)
	if err != nil || len(turns) == 0 {
		return ""
	}
	return string(stadogit.TurnTagRef(id, turns[len(turns)-1].Turn))
}
