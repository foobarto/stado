package main

// `stado session logs <id>` — render a session's trace-ref commits
// as a scannable per-tool-call feed. Answers "what did this session
// actually do?" for debugging without dropping to raw git log.
//
// Related commands:
//   - `stado audit export` — JSONL for SIEM ingestion (machine)
//   - `stado session show`  — per-session summary (header)
//   - `stado stats`         — aggregated cost/tokens (everything)
//
// logs fills the gap between audit export (too much JSON) and
// session show (too brief) — one scannable line per tool call
// with the fields users actually want: time, tool, args, outcome.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

var (
	logsLimit    int
	logsFollow   bool
	logsPollFreq time.Duration
)

var sessionLogsCmd = &cobra.Command{
	Use:     "logs <id>",
	Aliases: []string{"log"},
	Short:   "Tail a session's tool-call audit log (trace ref → human feed)",
	Long: "Walks the session's trace ref (newest-first) and renders each\n" +
		"tool-call commit as a one-line entry: time · tool(arg) · tokens ·\n" +
		"cost · duration. Errors are marked visibly.\n\n" +
		"Use --limit N to cap the number of entries. Piping to head/tail\n" +
		"also works — stdout is line-oriented.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		id, err := resolveSessionID(cfg, args[0])
		if err != nil {
			return fmt.Errorf("logs: %w", err)
		}
		useColour := useColor(os.Stdout)

		// Initial dump (newest-first) up to --limit. Follow-mode
		// starts its polling loop from the current tip, so we
		// remember the tip before dumping to avoid re-printing what
		// the dump already showed.
		head, _ := sc.ResolveRef(stadogit.TraceRef(id))
		lastSeen := head
		if head.IsZero() {
			fmt.Fprintf(os.Stderr, "(session %s has no trace commits yet)\n", id)
			if !logsFollow {
				return nil
			}
		} else {
			dumpLogHistory(sc, head, logsLimit, useColour)
		}
		if !logsFollow {
			return nil
		}
		// Follow loop. Polls the trace ref tip at logsPollFreq,
		// prints any new commits in forward (chronological) order,
		// exits on Ctrl+C via cobra's context.
		ctx := cmd.Context()
		if ctx == nil {
			ctx = contextBackground()
		}
		ticker := time.NewTicker(logsPollFreq)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				tip, err := sc.ResolveRef(stadogit.TraceRef(id))
				if err != nil || tip.IsZero() || tip == lastSeen {
					continue
				}
				printNewCommitsForward(sc, tip, lastSeen, useColour)
				lastSeen = tip
			}
		}
	},
}

// dumpLogHistory walks backwards from head printing entries, up to
// limit. Factored out so the follow path can skip it when tailing
// from-the-start.
func dumpLogHistory(sc *stadogit.Sidecar, head plumbingHashType, limit int, colour bool) {
	repo := sc.Repo()
	cur := head
	count := 0
	seen := map[string]bool{}
	for !cur.IsZero() {
		if seen[cur.String()] {
			break
		}
		seen[cur.String()] = true
		c, err := object.GetCommit(repo.Storer, cur)
		if err != nil {
			return
		}
		printLogEntry(c, colour)
		count++
		if limit > 0 && count >= limit {
			break
		}
		if len(c.ParentHashes) == 0 {
			break
		}
		cur = c.ParentHashes[0]
	}
}

// printNewCommitsForward walks from newTip back toward lastSeen,
// collects the range, then prints in reverse (oldest-first) so the
// follow-feed reads chronologically. Stops when it hits lastSeen
// (the previous tip) or walks off the root.
func printNewCommitsForward(sc *stadogit.Sidecar, newTip, lastSeen plumbingHashType, colour bool) {
	repo := sc.Repo()
	var chain []*object.Commit
	cur := newTip
	for !cur.IsZero() && cur != lastSeen {
		c, err := object.GetCommit(repo.Storer, cur)
		if err != nil {
			return
		}
		chain = append(chain, c)
		if len(c.ParentHashes) == 0 {
			break
		}
		cur = c.ParentHashes[0]
	}
	// Print in reverse so the oldest-new commit shows first.
	for i := len(chain) - 1; i >= 0; i-- {
		printLogEntry(chain[i], colour)
	}
}

// printLogEntry renders one trace commit as:
//   YYYY-MM-DD HH:MM · <tool>(<short-arg>): <summary> · <tokens-in>/<tokens-out>tok <cost> <duration>
//
// Colour (when enabled): error commits in red, title in default,
// stats in dim. Whole line fits in 120 cols for most calls.
func printLogEntry(c *object.Commit, colour bool) {
	title, trailers := parseCommitMessage(c.Message)
	when := c.Author.When.Local().Format("2006-01-02 15:04")
	tool := trailers["Tool"]
	tokensIn := atoiSafe(trailers["Tokens-In"])
	tokensOut := atoiSafe(trailers["Tokens-Out"])
	cost := atofSafe(trailers["Cost-USD"])
	durMs := atoi64Safe(trailers["Duration-Ms"])
	errMsg := trailers["Error"]
	_ = tool // not needed; title carries tool(arg) form

	line := fmt.Sprintf("%s · %s", when, title)
	// Append the stats tail when any data available. Keep silent
	// otherwise — trailers were optional in early commits.
	if tokensIn > 0 || tokensOut > 0 || cost > 0 || durMs > 0 {
		line += fmt.Sprintf("  ·  %d/%dtok  %s  %s",
			tokensIn, tokensOut, fmtCost(cost), fmtMs(durMs))
	}
	if errMsg != "" {
		line += "  ✗ " + errMsg
	}

	if colour {
		if errMsg != "" {
			fmt.Println("\x1b[31m" + line + "\x1b[0m")
		} else {
			fmt.Println(line)
		}
		return
	}
	fmt.Println(line)
}

// plumbingHashType is a local alias for go-git's plumbing.Hash so
// helper signatures stay readable without re-importing at every
// site.
type plumbingHashType = plumbing.Hash

func contextBackground() context.Context {
	return context.Background()
}

func init() {
	sessionLogsCmd.Flags().IntVar(&logsLimit, "limit", 0,
		"Cap entries (0 = unlimited, newest first). Pipe through head/tail to scope differently.")
	sessionLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false,
		"After the initial dump, keep watching the trace ref and print new commits as they land.")
	sessionLogsCmd.Flags().DurationVar(&logsPollFreq, "interval", 500*time.Millisecond,
		"Follow-mode poll frequency.")
	sessionLogsCmd.ValidArgsFunction = completeSessionIDs
	sessionCmd.AddCommand(sessionLogsCmd)
}
