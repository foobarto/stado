package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/workdirpath"
)

// Phase 9.4/9.5 — parallel-agent CLI.
//
// stado's "agents" are per-session worktrees; Phase 2.7's `stado session
// fork` already spawns an independent lineage off an existing session's
// tree head. This file adds the ergonomic layer: list everything currently
// active, kill a rogue one, attach to look at what it's doing.

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "Manage parallel agent sessions (PLAN §9.4)",
	Long: `A stado "agent" is a session with a running worktree. Multiple agents can
run on the same repo in parallel without clobbering each other — each owns
its own worktree directory and its own tree/trace refs in the sidecar.

  stado session fork <id>   # spawn a new agent from an existing session
  stado agents list         # show everything active
  stado agents kill <id>    # signal the owning process + remove worktree`,
}

var agentsListAll bool

var agentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active agent sessions with worktree + tree tip + owning PID if known",
	Long: "Default view is 'actually useful agents': rows with a live PID\n" +
		"or committed tree/trace content. Stale rows (PID dead, no refs)\n" +
		"are hidden and summarised in a footer. --all shows everything.",
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
		// Also pick up sessions that have a worktree but no commits yet.
		if entries, err := os.ReadDir(cfg.WorktreeDir()); err == nil {
			seen := map[string]bool{}
			for _, id := range ids {
				seen[id] = true
			}
			for _, e := range entries {
				if e.IsDir() && !seen[e.Name()] && stadogit.ValidateSessionID(e.Name()) == nil {
					ids = append(ids, e.Name())
				}
			}
			sort.Strings(ids)
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "(no agents)")
			return nil
		}
		hidden := 0
		for _, id := range ids {
			wt, err := worktreePathForID(cfg.WorktreeDir(), id)
			if err != nil {
				hidden++
				continue
			}
			alive := "-"
			liveAgent := false
			if pid := readPidFile(wt); pid > 0 {
				if processAlive(pid) {
					alive = fmt.Sprintf("pid=%d", pid)
					liveAgent = true
				} else {
					alive = fmt.Sprintf("pid=%d(stale)", pid)
				}
			}
			tree, _ := sc.ResolveRef(stadogit.TreeRef(id))
			trace, _ := sc.ResolveRef(stadogit.TraceRef(id))
			hasContent := !tree.IsZero() || !trace.IsZero()

			// Hide stale + empty rows unless --all. An agent row is
			// worth showing when: its process is alive, OR it has
			// committed something. Everything else is noise from
			// aborted runs.
			if !agentsListAll && !liveAgent && !hasContent {
				hidden++
				continue
			}
			fmt.Printf("%s\t%s\ttree=%s\ttrace=%s\n",
				id, alive, shortHash(tree), shortHash(trace))
		}
		if hidden > 0 {
			fmt.Fprintf(os.Stderr,
				"\n(%d stale/empty agent(s) hidden — use `stado agents list --all` to see them, or `stado session gc --apply` to clean up)\n",
				hidden)
		}
		return nil
	},
}

var agentsKillCmd = &cobra.Command{
	Use:   "kill <id>",
	Short: "Terminate an agent: signal its process (if known) + remove its worktree",
	Args:  cobra.ExactArgs(1),
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
		if err := workdirpath.RemoveAllNoSymlink(wt); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}
		fmt.Fprintln(os.Stderr, "killed", id)
		return nil
	},
}

var agentsAttachCmd = &cobra.Command{
	Use:   "attach <id>",
	Short: "Print the agent's worktree path for shell composition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		wt, err := worktreePathForID(cfg.WorktreeDir(), args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(wt); err != nil {
			return fmt.Errorf("attach: %w", err)
		}
		fmt.Println(wt)
		return nil
	},
}

func init() {
	agentsCmd.AddCommand(agentsListCmd, agentsKillCmd, agentsAttachCmd)
	agentsListCmd.Flags().BoolVar(&agentsListAll, "all", false,
		"Include stale/empty agent rows (hidden by default)")
	rootCmd.AddCommand(agentsCmd)
}

// readPidFile returns the pid stored at <worktree>/.stado-pid if present,
// or 0. stado TUI / stado run write their pid there on startup (wired in
// a follow-up — this reader works without the writer).
func readPidFile(worktree string) int {
	return runtime.ReadSessionPID(worktree)
}

func shortHash(h plumbing.Hash) string {
	if h.IsZero() {
		return "-"
	}
	return h.String()[:7]
}
