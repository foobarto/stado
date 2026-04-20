package main

// `stado stats` — cost + usage dashboard derived from commit trailers.
//
// Source of truth is the git-native trace refs stado writes for every
// tool call (CommitMeta in internal/state/git). That keeps stats
// zero-dependency on the OTel collector — works offline, works
// airgap, works when the user never ran a collector at all.
//
// Opencode/pi parity motivation — dogfood gap #3 (research 2026-04-20).

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

var (
	statsDays      int
	statsSession   string
	statsModel     string
	statsShowTools bool
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Cost + usage dashboard — tokens, cost, tool calls per session/model",
	Long: "Walks the sidecar's trace refs to aggregate token and cost usage from\n" +
		"commit trailers (Tokens-In/Tokens-Out/Cost-USD/Model). Source is the\n" +
		"git-native audit log, not the OTel exporter — works offline and airgap.\n\n" +
		"Default window: 7 days. Filter with --days, --session, --model. Use\n" +
		"--tools to include a per-tool breakdown in addition to the totals.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		cutoff := time.Now().Add(-time.Duration(statsDays) * 24 * time.Hour)

		ids, err := listSessions(sc)
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}
		if statsSession != "" {
			ids = filterStringSlice(ids, statsSession)
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "(no sessions in window)")
			return nil
		}

		agg := newStatsAgg()
		for _, id := range ids {
			if err := walkSessionForStats(sc, id, cutoff, agg); err != nil {
				fmt.Fprintf(os.Stderr, "stats: walk %s: %v\n", id, err)
			}
		}
		if agg.empty() {
			fmt.Fprintln(os.Stderr, "(no tool calls in window)")
			return nil
		}
		renderStats(os.Stdout, agg, statsShowTools)
		return nil
	},
}

// statsAgg holds the accumulation as we walk commits. Keyed by model so
// the usual shape ("here's what I spent on claude-sonnet vs gpt-4o")
// falls out without re-aggregating.
type statsAgg struct {
	totalCalls int
	totalIn    int
	totalOut   int
	totalCost  float64
	totalMs    int64
	byModel    map[string]*modelStats
	byTool     map[string]*toolStats
}

type modelStats struct {
	calls int
	in    int
	out   int
	cost  float64
}

type toolStats struct {
	calls int
	ms    int64
}

func newStatsAgg() *statsAgg {
	return &statsAgg{
		byModel: map[string]*modelStats{},
		byTool:  map[string]*toolStats{},
	}
}

func (a *statsAgg) empty() bool { return a.totalCalls == 0 }

// walkSessionForStats iterates the session's trace ref and feeds each
// commit into the aggregator. Silently ignores sessions whose trace
// ref is missing (e.g. a session that never did anything).
func walkSessionForStats(sc *stadogit.Sidecar, sessionID string, cutoff time.Time, agg *statsAgg) error {
	head, err := sc.ResolveRef(stadogit.TraceRef(sessionID))
	if err != nil || head.IsZero() {
		return nil // no trace ref yet; nothing to count
	}
	cur := head
	seen := map[string]bool{}
	for !cur.IsZero() {
		if seen[cur.String()] {
			break
		}
		seen[cur.String()] = true

		c, err := object.GetCommit(sc.Repo().Storer, cur)
		if err != nil {
			return err
		}
		if c.Author.When.Before(cutoff) {
			break // commits are author-time monotonic going back; safe to stop
		}
		absorb(agg, c)
		if len(c.ParentHashes) == 0 {
			break
		}
		cur = c.ParentHashes[0]
	}
	return nil
}

// absorb parses the trailers on one commit and adds the numeric fields
// to the aggregator. Filter knobs (--model) are applied here so the
// caller's walk loop stays simple.
func absorb(agg *statsAgg, c *object.Commit) {
	title, trailers := parseCommitMessage(c.Message)
	_ = title
	model := trailers["Model"]
	if statsModel != "" && model != statsModel {
		return
	}
	tool := trailers["Tool"]
	if tool == "" {
		return // not a tool-call trace commit
	}
	in := atoiSafe(trailers["Tokens-In"])
	out := atoiSafe(trailers["Tokens-Out"])
	cost := atofSafe(trailers["Cost-USD"])
	ms := atoi64Safe(trailers["Duration-Ms"])

	agg.totalCalls++
	agg.totalIn += in
	agg.totalOut += out
	agg.totalCost += cost
	agg.totalMs += ms

	if model != "" {
		ms := agg.byModel[model]
		if ms == nil {
			ms = &modelStats{}
			agg.byModel[model] = ms
		}
		ms.calls++
		ms.in += in
		ms.out += out
		ms.cost += cost
	}
	ts := agg.byTool[tool]
	if ts == nil {
		ts = &toolStats{}
		agg.byTool[tool] = ts
	}
	ts.calls++
	ts.ms += ms
}

// renderStats writes the human table to w. Keeps the format terse:
// per-model table, summary line, optional per-tool table.
func renderStats(w interface {
	Write(p []byte) (int, error)
	WriteString(s string) (int, error)
}, agg *statsAgg, withTools bool) {
	fmt.Fprintf(w, "Window: last %d day(s)", statsDays)
	if statsSession != "" {
		fmt.Fprintf(w, "  session=%s", statsSession)
	}
	if statsModel != "" {
		fmt.Fprintf(w, "  model=%s", statsModel)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	// By-model table.
	models := make([]string, 0, len(agg.byModel))
	for m := range agg.byModel {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		return agg.byModel[models[i]].cost > agg.byModel[models[j]].cost
	})

	fmt.Fprintf(w, "%-32s  %6s  %10s  %10s  %10s\n",
		"MODEL", "CALLS", "TOKENS-IN", "TOKENS-OUT", "COST-USD")
	for _, m := range models {
		s := agg.byModel[m]
		fmt.Fprintf(w, "%-32s  %6d  %10d  %10d  %10s\n",
			truncString(m, 32), s.calls, s.in, s.out, fmtCost(s.cost))
	}
	if len(models) == 0 {
		fmt.Fprintf(w, "%-32s  %6s  %10s  %10s  %10s\n",
			"(no model trailers)", "-", "-", "-", "-")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "TOTAL  calls=%d  tokens-in=%d  tokens-out=%d  cost=%s  time=%s\n",
		agg.totalCalls, agg.totalIn, agg.totalOut, fmtCost(agg.totalCost), fmtMs(agg.totalMs))

	if !withTools || len(agg.byTool) == 0 {
		return
	}
	fmt.Fprintln(w)
	// By-tool table.
	tools := make([]string, 0, len(agg.byTool))
	for t := range agg.byTool {
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool {
		return agg.byTool[tools[i]].calls > agg.byTool[tools[j]].calls
	})
	fmt.Fprintf(w, "%-24s  %6s  %10s\n", "TOOL", "CALLS", "TIME")
	for _, t := range tools {
		s := agg.byTool[t]
		fmt.Fprintf(w, "%-24s  %6d  %10s\n", truncString(t, 24), s.calls, fmtMs(s.ms))
	}
}

// parseCommitMessage mirrors internal/audit/export.go's parseMessage.
// Copied here to keep cmd/stado free of an audit import cycle; the
// logic is trivial enough that drift risk is low.
func parseCommitMessage(msg string) (title string, trailers map[string]string) {
	trailers = map[string]string{}
	lines := strings.Split(msg, "\n")
	var titleDone bool
	for _, line := range lines {
		if !titleDone {
			if line == "" {
				titleDone = true
				continue
			}
			if title == "" {
				title = line
			}
			continue
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			if k != "" && k != "Signature" {
				trailers[k] = v
			}
		}
	}
	return title, trailers
}

// filterStringSlice returns a one- or zero-element slice matching needle.
func filterStringSlice(ss []string, needle string) []string {
	for _, s := range ss {
		if s == needle {
			return []string{s}
		}
	}
	return nil
}

// fmtCost renders a USD figure with precision that makes sense for the
// magnitude (subdollar → 4 digits so $0.0012 is readable).
func fmtCost(c float64) string {
	if c >= 1 {
		return fmt.Sprintf("$%.2f", c)
	}
	return fmt.Sprintf("$%.4f", c)
}

// fmtMs renders a ms duration compactly — s/m once we're past 1000ms.
func fmtMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1fm", float64(ms)/60_000)
}

func atoiSafe(s string) int {
	var n int
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < '0' || b > '9' {
			if i == 0 && b == '-' {
				continue
			}
			return 0
		}
		n = n*10 + int(b-'0')
	}
	return n
}

func atoi64Safe(s string) int64 { return int64(atoiSafe(s)) }

func atofSafe(s string) float64 {
	var out float64
	var seenDot bool
	var frac float64 = 1
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b == '.':
			seenDot = true
		case b >= '0' && b <= '9':
			d := float64(b - '0')
			if seenDot {
				frac *= 10
				out += d / frac
			} else {
				out = out*10 + d
			}
		default:
			// Skip sign / other; keeps the parser tolerant of
			// trailers like "0.0012" without pulling in strconv.
			if i == 0 && b == '-' {
				continue
			}
			return out
		}
	}
	return out
}

func truncString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func init() {
	statsCmd.Flags().IntVar(&statsDays, "days", 7, "Window in days to aggregate")
	statsCmd.Flags().StringVar(&statsSession, "session", "", "Restrict to one session id")
	statsCmd.Flags().StringVar(&statsModel, "model", "", "Restrict to one model id (matches Model: trailer)")
	statsCmd.Flags().BoolVar(&statsShowTools, "tools", false, "Include per-tool breakdown")
	rootCmd.AddCommand(statsCmd)
}
