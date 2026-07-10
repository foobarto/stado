package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/workdirpath"
)

// sessionKillCmd is the operational stop-and-clean for a running session:
// signal the owning process (if any), then remove its worktree — but leave the
// sidecar history intact. This is the behaviour formerly exposed as
// `stado agents kill`. The `agents` surface was folded into `session` because
// its `list`/`attach` duplicated `session list` (STATUS=live) / `session attach`
// and only this process-signalling path was unique. `session delete` is the
// destructive sibling: it purges refs too; `kill` keeps them.
var sessionKillCmd = &cobra.Command{
	Use:   "kill <id>",
	Short: "Signal a session's running process (if any) and remove its worktree, keeping history",
	Long: "Operational stop-and-clean. Reads `<worktree>/.stado-pid`, sends a\n" +
		"termination signal to that process when it is still alive, then removes\n" +
		"the worktree directory. Sidecar history (refs/sessions/<id>/*) is left\n" +
		"intact — use `session delete` when you also want to purge the refs.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		id := args[0]
		wt, err := worktreePathForID(cfg.WorktreeDir(), id)
		if err != nil {
			return err
		}
		if pid := readPidFile(wt); pid > 0 {
			if err := terminateProcess(pid); err == nil {
				fmt.Fprintf(os.Stderr, "sent termination signal to pid %d\n", pid)
			}
		}
		if err := workdirpath.NewUserConfigResolver().RemoveAll(wt); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}
		fmt.Fprintln(os.Stderr, "killed", id)
		return nil
	},
}

// readPidFile returns the pid stored at <worktree>/.stado-pid if present,
// or 0. stado TUI / stado run write their pid there on startup. Symlink-safe:
// a `.stado-pid` symlink pointing outside the worktree reads as 0.
func readPidFile(worktree string) int {
	return runtime.ReadSessionPID(worktree)
}
