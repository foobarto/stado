package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/tui"
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

var sessionListAll bool

var sessionListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List sessions for the current repo",
	Long: "By default, zero-turn + zero-message sessions are hidden so the\n" +
		"list surfaces meaningful work. Pass --all to see every session\n" +
		"on disk, including empties left over from aborted runs.\n\n" +
		"Hidden rows are summarised as a footer line with a pointer to\n" +
		"`session gc --apply` so cleanup is one copy-paste away.",
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
		// Gather metadata for all sessions. Slower than the old
		// one-liner but the information matters now that
		// `stado session resume <id>` needs users to pick out "which
		// session?" from a list of UUIDs.
		rows := make([]runtime.SessionSummary, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, runtime.SummariseSession(cfg.WorktreeDir(), sc, id))
		}

		// Filter: default behaviour hides zero-turn + zero-message
		// sessions so `session list` surfaces meaningful work, not
		// test-run detritus. --all restores the old behaviour when
		// someone needs to see the full picture.
		hidden := 0
		visible := rows
		if !sessionListAll {
			visible = visible[:0]
			for _, r := range rows {
				// Turns == 0 means no work boundary was ever committed,
				// even if an orphan user message got persisted. Those
				// sessions can't be meaningfully resumed; hide them.
				if r.Turns == 0 && r.Compactions == 0 {
					hidden++
					continue
				}
				visible = append(visible, r)
			}
		}

		// Columns: ID | last-active | turns | msgs | compactions | status | description
		// Aligned so `session list | less -S` stays scannable. The
		// DESCRIPTION column is last because its width varies; anything
		// past STATUS soft-wraps gracefully.
		const header = "SESSION ID                              LAST ACTIVE           TURNS  MSGS  COMPACT  STATUS     DESCRIPTION\n"
		fmt.Print(header)
		use := useColor(os.Stdout)
		for _, r := range visible {
			statusPadded := fmt.Sprintf("%-9s", r.Status)
			if use {
				statusPadded = colorizeStatus(r.Status, statusPadded)
			}
			fmt.Printf("%-40s %-21s %5d  %4d  %7d  %s  %s\n",
				r.ID, r.LastActiveFormatted(), r.Turns, r.Msgs, r.Compactions, statusPadded, r.Description)
		}
		if hidden > 0 {
			fmt.Fprintf(os.Stderr,
				"\n(%d empty session(s) hidden — use `stado session list --all` to see them, or `stado session gc --apply` to clean up)\n",
				hidden)
		}
		return nil
	},
}

// (sessionRow + summariseSession live in internal/runtime so the TUI
// can share them — see runtime.SessionSummary / runtime.SummariseSession.)

// sessionGCCmd sweeps zero-turn, zero-message, zero-compaction
// sessions older than --older-than. Dogfood #6: `run --prompt`, bare
// `session new`, and each headless session leaves a session on disk;
// over a week of scripted use they pile up into session-list noise.
// GC touches only sessions that clearly never did any work.
var sessionGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Sweep zero-turn sessions older than --older-than (dry-run by default)",
	Long: "Scans every session (ref-backed + worktree-backed) and removes those\n" +
		"that have: 0 turn tags AND 0 persisted conversation messages AND 0\n" +
		"compaction markers AND a worktree mtime older than --older-than. Any\n" +
		"session currently live (valid .stado-pid) is skipped regardless of\n" +
		"age. Default is dry-run — pass --apply to actually delete.",
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
			return fmt.Errorf("list sessions: %w", err)
		}
		// Also scan the worktree dir for UUID-looking directories the
		// sidecar may not know about — dogfood showed `run --prompt`
		// can leave a worktree without a trace ref.
		if entries, err := os.ReadDir(cfg.WorktreeDir()); err == nil {
			seen := map[string]struct{}{}
			cwd, _ := os.Getwd()
			currentRepo := findRepoRoot(cwd)
			for _, id := range ids {
				seen[id] = struct{}{}
			}
			for _, e := range entries {
				if e.IsDir() {
					if runtime.ReadUserRepoPin(filepath.Join(cfg.WorktreeDir(), e.Name())) != currentRepo {
						continue
					}
					if _, ok := seen[e.Name()]; !ok {
						ids = append(ids, e.Name())
					}
				}
			}
			sort.Strings(ids)
		}

		cutoff := time.Now().Add(-sessionGCOlderThan)
		var toDelete []string
		var skipped int
		for _, id := range ids {
			summary := runtime.SummariseSession(cfg.WorktreeDir(), sc, id)
			if summary.Status == "live" {
				skipped++
				continue
			}
			if summary.Turns > 0 || summary.Msgs > 0 || summary.Compactions > 0 {
				skipped++
				continue
			}
			wt, err := worktreePathForID(cfg.WorktreeDir(), id)
			if err != nil {
				skipped++
				continue
			}
			if info, err := os.Stat(wt); err == nil {
				if info.ModTime().After(cutoff) {
					skipped++
					continue
				}
			}
			toDelete = append(toDelete, id)
		}

		if len(toDelete) == 0 {
			fmt.Fprintf(os.Stderr, "no candidates (older than %s). %d session(s) skipped.\n",
				sessionGCOlderThan, skipped)
			return nil
		}

		fmt.Fprintf(os.Stderr, "%d candidate(s) older than %s:\n",
			len(toDelete), sessionGCOlderThan)
		for _, id := range toDelete {
			fmt.Fprintln(os.Stderr, "  "+id)
		}
		if !sessionGCApply {
			fmt.Fprintln(os.Stderr, "(dry run — rerun with --apply to delete)")
			return nil
		}
		var errs int
		for _, id := range toDelete {
			wt, err := worktreePathForID(cfg.WorktreeDir(), id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid session id %q: %v\n", id, err)
				errs++
				continue
			}
			if err := sc.DeleteSessionRefs(id); err != nil {
				fmt.Fprintf(os.Stderr, "delete refs %s: %v\n", id, err)
				errs++
				continue
			}
			if err := os.RemoveAll(wt); err != nil {
				fmt.Fprintf(os.Stderr, "remove worktree %s: %v\n", id, err)
				errs++
				continue
			}
			fmt.Fprintln(os.Stderr, "deleted", id)
		}
		if errs > 0 {
			return fmt.Errorf("%d deletion error(s)", errs)
		}
		return nil
	},
}

var (
	sessionGCOlderThan time.Duration
	sessionGCApply     bool
)

var sessionDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete a session (refs + worktree)",
	Args:    cobra.ExactArgs(1),
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
		wt, err := worktreePathForID(cfg.WorktreeDir(), id)
		if err != nil {
			return err
		}
		refsExisted, _ := sc.SessionHasRefs(id)
		_, worktreeErr := os.Stat(wt)
		worktreeExisted := worktreeErr == nil

		if err := sc.DeleteSessionRefs(id); err != nil {
			return fmt.Errorf("delete refs: %w", err)
		}
		if err := os.RemoveAll(wt); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}
		switch {
		case refsExisted || worktreeExisted:
			fmt.Fprintln(os.Stderr, "deleted", id)
		default:
			fmt.Fprintln(os.Stderr, id, "already deleted (or never existed — no refs or worktree found)")
		}
		return nil
	},
}

// sessionDescribeCmd lets the user attach a human-readable label to a
// session. Stored in `<worktree>/.stado/description`; shown by
// `session list` (description column) and `session show`. Writing "" /
// `--clear` removes the label.
var sessionDescribeCmd = &cobra.Command{
	Use:   "describe <id> [text]",
	Short: "Attach (or clear) a human-readable description for a session",
	Long: "Sessions are identified by UUIDs which are hard to recall. Describe\n" +
		"lets you give a session a short human label — visible in `session list`\n" +
		"and `session show`. Stored at `<worktree>/.stado/description`.\n\n" +
		"  stado session describe <id> \"react refactor\"   # set\n" +
		"  stado session describe <id> --clear             # remove\n" +
		"  stado session describe <id>                     # print current",
	Args: cobra.RangeArgs(1, 2),
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
		if _, err := os.Stat(wt); err != nil {
			return fmt.Errorf("describe: session %s not found (no worktree at %s)", id, wt)
		}
		if describeClear {
			if err := runtime.WriteDescription(wt, ""); err != nil {
				return fmt.Errorf("describe: clear: %w", err)
			}
			fmt.Fprintln(os.Stderr, "cleared description for", id)
			return nil
		}
		if len(args) == 1 {
			// Read mode.
			d := runtime.ReadDescription(wt)
			if d == "" {
				fmt.Fprintln(os.Stderr, "(no description set)")
			} else {
				fmt.Println(d)
			}
			return nil
		}
		text := strings.TrimSpace(args[1])
		if text == "" {
			return fmt.Errorf("describe: empty text — use --clear to remove the description")
		}
		if err := runtime.WriteDescription(wt, text); err != nil {
			return fmt.Errorf("describe: write: %w", err)
		}
		fmt.Fprintf(os.Stderr, "described %s: %q\n", id, text)
		return nil
	},
}

var describeClear bool

func init() {
	sessionListCmd.Flags().BoolVar(&sessionListAll, "all", false,
		"Include zero-turn / zero-message sessions (hidden by default)")
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
		wt, err := worktreePathForID(cfg.WorktreeDir(), args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(wt); err != nil {
			return fmt.Errorf("attach: session %s not found (no worktree at %s)", args[0], wt)
		}
		fmt.Println(wt)
		return nil
	},
}

// sessionResumeCmd is the one-shot equivalent of
//
//	cd $(stado session attach <id>) && stado
//
// — changes into the session's worktree so `runtime.OpenSession`'s
// resume-on-cwd logic reattaches to the same session ID, then
// launches the TUI inline. Fast path for the common "I killed stado,
// I want it back" workflow.
var sessionResumeCmd = &cobra.Command{
	Use:   "resume <id-or-label>",
	Short: "Attach to an existing session and launch the TUI in its worktree",
	Long: "Changes into the session's worktree (equivalent to\n" +
		"`cd $(stado session attach <id>)`) and boots the TUI inline.\n" +
		"The TUI resumes the session: same ID, same git refs, conversation\n" +
		"history replayed from the worktree's `.stado/conversation.jsonl`.\n\n" +
		"The lookup argument can be:\n" +
		"  · a full session UUID\n" +
		"  · a unique UUID prefix (≥8 chars)\n" +
		"  · a case-insensitive substring match of a session's description\n" +
		"Ambiguous matches error out listing candidates so you can narrow.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		id, err := resolveSessionID(cfg, args[0])
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		wt, err := worktreePathForID(cfg.WorktreeDir(), id)
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		if err := os.Chdir(wt); err != nil {
			return fmt.Errorf("resume: chdir %s: %w", wt, err)
		}
		// Launch the same entry point `stado` uses for its default
		// TUI. runtime.OpenSession sees that cwd is a session
		// worktree and takes the resume-on-cwd branch.
		return withTelemetry(cmd.Context(), cfg, func(context.Context) error {
			return tui.Run(cfg)
		})
	},
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Print session refs + worktree + turn count + latest commit summary",
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
		wt, err := worktreePathForID(cfg.WorktreeDir(), id)
		if err != nil {
			return err
		}
		fmt.Printf("session:  %s\n", id)
		if desc := runtime.ReadDescription(wt); desc != "" {
			fmt.Printf("label:    %s\n", desc)
		}
		fmt.Printf("worktree: %s\n", wt)
		for _, pair := range []struct {
			name string
			ref  refMakerSession
		}{
			{"tree", stadogit.TreeRef},
			{"trace", stadogit.TraceRef},
		} {
			head, err := sc.ResolveRef(pair.ref(id))
			if err != nil {
				fmt.Printf("%-6s    (unset)\n", pair.name)
				continue
			}
			fmt.Printf("%-6s    %s\n", pair.name, head.String()[:12])
		}

		// Turn-boundary tags. Empty sessions report 0 cleanly.
		turns, err := sc.ListTurnRefs(id)
		if err == nil {
			fmt.Printf("turns     %d\n", len(turns))
			if n := len(turns); n > 0 {
				last := turns[n-1]
				// Truncate long summaries so the output stays parseable.
				summary := textutil.StripControlChars(last.Summary)
				if len(summary) > 64 {
					summary = summary[:63] + "…"
				}
				fmt.Printf("latest    turns/%d  %s  %s\n",
					last.Turn, last.When.Format("2006-01-02 15:04"), summary)
			}
		}

		// Trace-ref depth: how many tool calls were audited. Mirrors
		// `git rev-list --count refs/sessions/<id>/trace` without
		// shelling out.
		if n, err := countCommits(sc, stadogit.TraceRef(id)); err == nil {
			fmt.Printf("audit     %d tool call(s) on trace ref\n", n)
		}

		// Cost/tokens summary from the trace-ref trailers (same
		// source as `stado stats`). Window is the session's entire
		// lifetime since we're scoped to one session — no cutoff.
		agg := newStatsAgg()
		_ = walkSessionForStats(sc, id, time.Time{}, agg)
		if agg.totalCalls > 0 {
			fmt.Printf("usage     %d call(s)  tokens=%d/%d  cost=%s  time=%s\n",
				agg.totalCalls, agg.totalIn, agg.totalOut, fmtCost(agg.totalCost), fmtMs(agg.totalMs))
		}

		// Compaction markers — PLAN §11.3.6. Surfaces which turn
		// ranges have been collapsed, when, and by whom. Walks tree
		// ref newest-first; rolled back or forked-over compactions
		// simply don't appear in this listing.
		if markers, err := sc.ListCompactions(id); err == nil && len(markers) > 0 {
			fmt.Printf("compactions  %d event(s):\n", len(markers))
			for _, m := range markers {
				title := textutil.StripControlChars(m.Title)
				if len(title) > 60 {
					title = title[:59] + "…"
				}
				shaShort := m.CommitHash.String()[:12]
				when := m.At
				if len(when) > 19 {
					when = when[:19] // trim to minute precision
				}
				by := ""
				if m.By != "" {
					by = " · by " + textutil.StripControlChars(m.By)
				}
				fmt.Printf("  %s  turns %d..%d (%d collapsed)%s\n",
					shaShort, m.FromTurn, m.ToTurn, m.TurnsTotal, by)
				if when != "" {
					fmt.Printf("             at %s\n", when)
				}
				if title != "" {
					fmt.Printf("             %s\n", title)
				}
				if m.RawLogSHA != "" {
					raw := m.RawLogSHA
					if len(raw) > 19 {
						raw = raw[:19]
					}
					fmt.Printf("             raw-log %s\n", raw)
				}
			}
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

func init() {
	sessionGCCmd.Flags().DurationVar(&sessionGCOlderThan, "older-than", 24*time.Hour,
		"Skip sessions whose worktree was touched more recently than this")
	sessionGCCmd.Flags().BoolVar(&sessionGCApply, "apply", false,
		"Actually delete. Default is dry-run (list candidates only)")
	sessionDescribeCmd.Flags().BoolVar(&describeClear, "clear", false,
		"Remove the description instead of setting one")

	// Shell-completion for session IDs. Every subcommand that takes
	// an <id> first-positional gets the same completer so
	// `stado session <subcmd> <TAB>` lists extant sessions in bash/
	// zsh/fish. Subcommands whose first arg is something else (new,
	// list, gc, search) don't get a completer — ValidArgsFunction
	// default is "no completions" which matches cobra's built-in.
	idCompleter := completeSessionIDs
	sessionShowCmd.ValidArgsFunction = idCompleter
	sessionAttachCmd.ValidArgsFunction = idCompleter
	sessionResumeCmd.ValidArgsFunction = idCompleter
	sessionDeleteCmd.ValidArgsFunction = idCompleter
	sessionForkCmd.ValidArgsFunction = idCompleter
	sessionDescribeCmd.ValidArgsFunction = idCompleter
	sessionRevertCmd.ValidArgsFunction = idCompleter
	sessionLandCmd.ValidArgsFunction = idCompleter
	sessionTreeCmd.ValidArgsFunction = idCompleter
	sessionExportCmd.ValidArgsFunction = idCompleter
	sessionCompactCmd.ValidArgsFunction = idCompleter

	sessionCmd.AddCommand(
		sessionNewCmd, sessionListCmd, sessionDeleteCmd, sessionGCCmd, sessionForkCmd,
		sessionAttachCmd, sessionResumeCmd, sessionShowCmd, sessionLandCmd, sessionRevertCmd,
		sessionTreeCmd, sessionCompactCmd, sessionDescribeCmd, sessionSearchCmd,
	)
	rootCmd.AddCommand(sessionCmd)
}

// Silence "gogit imported but not used" — cobra command file uses it for
// error typing in case we later want to branch on ErrRepositoryNotExists.
var _ = gogit.ErrRepositoryNotExists
var _ = errors.New
