package main

// `stado usage` — aggregate token-usage metrics by model from the
// stadogit audit history. Each turn commit carries Tokens-In /
// Tokens-Out / Model / Cost-USD trailers (see
// internal/state/git/commit_meta.go). This subcommand walks every
// session in every sidecar repo under <state-dir>/sessions/, parses
// the trailers, optionally filters by time window, and prints a
// per-model aggregation.
//
// Time window is opt-in via --since / --until. Supports relative
// durations (24h, 7d, 1w, 1mo) and absolute dates (RFC3339 or
// YYYY-MM-DD).
//
// NOTE on data availability: the CommitMeta infrastructure reserves
// Tokens-In/Tokens-Out trailer slots but the current agent loop
// emits zeros for them on tool-call and turn_boundary commits. This
// subcommand reads what's persisted; once the agent loop populates
// real token counts on its turn commits (separate fix — the schema
// is wired, the data isn't yet), the report becomes meaningful.
// Until then `stado usage` will report "No turns recorded" against
// historical sessions even when sessions exist — expected.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

var (
	usageSinceFlag string
	usageUntilFlag string
	usageJSONFlag  bool
	usageBySession bool
)

var usageCmd = &cobra.Command{
	Use:   "usage [flags]",
	Short: "Aggregate token-usage metrics by model from session audit history",
	Long: "Walk every stadogit session under <state-dir>/sessions/, parse\n" +
		"per-turn commit trailers (Tokens-In, Tokens-Out, Model,\n" +
		"Cost-USD), and print an aggregation grouped by model.\n\n" +
		"Time-window filtering is opt-in via --since / --until. Both\n" +
		"accept relative durations (24h, 7d, 1w, 1mo, 1y) and absolute\n" +
		"dates (RFC3339 or YYYY-MM-DD). Without flags, the report\n" +
		"covers all recorded turns.\n\n" +
		"Examples:\n" +
		"  stado usage                          # all-time\n" +
		"  stado usage --since 24h              # last 24 hours\n" +
		"  stado usage --since 7d --until 1d    # the day-1-to-7 window\n" +
		"  stado usage --since 2026-04-01       # from a specific date\n" +
		"  stado usage --json                   # machine-readable\n" +
		"  stado usage --by-session             # break out per session\n",
	RunE: runUsage,
}

func init() {
	usageCmd.Flags().StringVar(&usageSinceFlag, "since", "",
		"Lower bound: duration (24h, 7d, 1w, 1mo, 1y) or date (YYYY-MM-DD or RFC3339)")
	usageCmd.Flags().StringVar(&usageUntilFlag, "until", "",
		"Upper bound (same syntax as --since)")
	usageCmd.Flags().BoolVar(&usageJSONFlag, "json", false,
		"Emit machine-readable JSON instead of the formatted table")
	usageCmd.Flags().BoolVar(&usageBySession, "by-session", false,
		"Break out per-session totals before the per-model aggregate")
	rootCmd.AddCommand(usageCmd)
}

// usageModelStats accumulates per-model totals across one report.
type usageModelStats struct {
	Model        string    `json:"model"`
	Turns        int       `json:"turns"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CacheHits    int       `json:"cache_hits"`
	CostUSD      float64   `json:"cost_usd"`
	First        time.Time `json:"first"`
	Last         time.Time `json:"last"`
}

// usageSessionStats holds per-session breakdowns when --by-session is set.
type usageSessionStats struct {
	SessionID    string  `json:"session_id"`
	Repo         string  `json:"repo"` // sidecar dir basename
	Turns        int     `json:"turns"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	// Models lists the distinct models seen in this session, sorted.
	Models []string `json:"models"`
}

func runUsage(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("usage: load config: %w", err)
	}

	since, until, err := parseUsageTimeWindow(usageSinceFlag, usageUntilFlag, time.Now())
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(cfg.StateDir(), "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No sessions yet — emit an empty report rather than error.
			return printUsageReport(nil, nil, since, until)
		}
		return fmt.Errorf("usage: read %s: %w", sessionsDir, err)
	}

	modelAgg := map[string]*usageModelStats{}
	var sessions []usageSessionStats

	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
			continue
		}
		sidecarPath := filepath.Join(sessionsDir, e.Name())
		// userRepoRoot is unused for read-only walking; pass the
		// sidecar path itself to satisfy OpenOrInitSidecar's
		// non-empty validation.
		sc, err := stadogit.OpenOrInitSidecar(sidecarPath, sidecarPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "usage: skip sidecar %s: %v\n", e.Name(), err)
			continue
		}
		ids, err := listAllSessionIDs(sc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "usage: list sessions in %s: %v\n", e.Name(), err)
			continue
		}
		for _, id := range ids {
			ss, err := walkSessionTrace(sc, id, since, until, modelAgg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "usage: walk %s/%s: %v\n", e.Name(), id, err)
				continue
			}
			if ss.Turns > 0 {
				ss.Repo = strings.TrimSuffix(e.Name(), ".git")
				sessions = append(sessions, ss)
			}
		}
	}

	return printUsageReport(modelAgg, sessions, since, until)
}

// listAllSessionIDs is the same logic as listSessions in
// session_lookup.go but inlined here so the usage subcommand stays
// independent of session-management state (compaction, gc, etc.).
func listAllSessionIDs(sc *stadogit.Sidecar) ([]string, error) {
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
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// walkSessionTrace walks the trace ref's commit chain for one session,
// parses trailers, and accumulates into modelAgg. Returns the
// per-session stats for --by-session output.
func walkSessionTrace(sc *stadogit.Sidecar, sessionID string, since, until time.Time, modelAgg map[string]*usageModelStats) (usageSessionStats, error) {
	ss := usageSessionStats{SessionID: sessionID}
	traceRef := stadogit.TraceRef(sessionID)
	head, err := sc.ResolveRef(traceRef)
	if err != nil {
		// No trace ref → session has only tree-side commits (e.g. a
		// freshly created session). Skip silently.
		return ss, nil
	}

	repo := sc.Repo()
	modelsInSession := map[string]struct{}{}
	cur := head
	for !cur.IsZero() {
		commit, err := repo.CommitObject(cur)
		if err != nil {
			break // chain terminated early; stop walking
		}

		when := commit.Author.When
		// Parse trailers ahead of the time-window check so we can
		// always advance the chain via .Parents.
		parsed := parseTrailers(commit.Message)

		// Time-window filter. Authoritative timestamp is the commit's
		// Author.When (set by stado at write time).
		inWindow := true
		if !since.IsZero() && when.Before(since) {
			inWindow = false
		}
		if !until.IsZero() && when.After(until) {
			inWindow = false
		}

		if inWindow {
			model := parsed["model"]
			if model == "" {
				model = "(unknown)"
			}
			tokensIn, _ := strconv.ParseInt(parsed["tokens_in"], 10, 64)
			tokensOut, _ := strconv.ParseInt(parsed["tokens_out"], 10, 64)
			costUSD, _ := strconv.ParseFloat(parsed["cost_usd"], 64)
			cacheHit := parsed["cache_hit"] == "true"

			// Only count turns that actually emitted token data —
			// skip purely-mutating commits (write/edit) that aren't
			// LLM turns.
			if tokensIn > 0 || tokensOut > 0 {
				stat := modelAgg[model]
				if stat == nil {
					stat = &usageModelStats{Model: model, First: when, Last: when}
					modelAgg[model] = stat
				}
				stat.Turns++
				stat.InputTokens += tokensIn
				stat.OutputTokens += tokensOut
				stat.CostUSD += costUSD
				if cacheHit {
					stat.CacheHits++
				}
				if when.Before(stat.First) {
					stat.First = when
				}
				if when.After(stat.Last) {
					stat.Last = when
				}
				ss.Turns++
				ss.InputTokens += tokensIn
				ss.OutputTokens += tokensOut
				ss.CostUSD += costUSD
				modelsInSession[model] = struct{}{}
			}
		}

		// Trace ref is single-parent; just take the first.
		if len(commit.ParentHashes) == 0 {
			break
		}
		cur = commit.ParentHashes[0]
	}

	for m := range modelsInSession {
		ss.Models = append(ss.Models, m)
	}
	sort.Strings(ss.Models)
	return ss, nil
}

// parsedTrailers holds the per-commit fields we care about; access
// trailing keys via the embedded map (lowercase, hyphens → underscores).
type parsedTrailers map[string]string

func parseTrailers(msg string) parsedTrailers {
	out := parsedTrailers{}
	// Trailers come after the first blank line, one per line: Key: Value
	parts := strings.SplitN(msg, "\n\n", 2)
	if len(parts) < 2 {
		return out
	}
	for _, line := range strings.Split(parts[1], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
		val := strings.TrimSpace(v)
		out[key] = val
	}
	return out
}

// parseUsageTimeWindow translates user-supplied --since / --until
// strings into time.Time bounds. Either may be empty (zero time
// disables that bound). Supports:
//   - relative durations: "24h", "7d", "1w", "1mo", "1y" — interpreted
//     as time.Now() minus the duration for --since (and minus the
//     duration for --until when the operator wants a "ago" window).
//   - absolute YYYY-MM-DD: parsed as midnight UTC.
//   - RFC3339: parsed as-is.
//
// `now` is injected for testability — production callers pass
// time.Now().
func parseUsageTimeWindow(sinceStr, untilStr string, now time.Time) (time.Time, time.Time, error) {
	since, err := parseUsageTime(sinceStr, now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("--since: %w", err)
	}
	until, err := parseUsageTime(untilStr, now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("--until: %w", err)
	}
	return since, until, nil
}

// parseUsageTime parses one bound string. Empty → zero time
// (disables that bound).
func parseUsageTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	// Relative form: digits + unit. Handle compound multi-unit shorthand
	// the stdlib doesn't (`1w`, `7d`, `1mo`, `1y`).
	if t, ok := parseRelativeDuration(s, now); ok {
		return t, nil
	}

	// stdlib durations (h, m, s).
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}

	// Absolute YYYY-MM-DD.
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}

	// RFC3339.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unrecognised time format %q (try 24h, 7d, 1w, YYYY-MM-DD, or RFC3339)", s)
}

// parseRelativeDuration handles "<N><unit>" where unit ∈
// {d, w, mo, y}. Returns (time, true) on a hit, (zero, false)
// otherwise so the caller can fall through to the stdlib parser.
func parseRelativeDuration(s string, now time.Time) (time.Time, bool) {
	// Split into leading digits + trailing unit.
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i == len(s) {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return time.Time{}, false
	}
	unit := s[i:]
	switch unit {
	case "d":
		return now.AddDate(0, 0, -n), true
	case "w":
		return now.AddDate(0, 0, -n*7), true
	case "mo":
		return now.AddDate(0, -n, 0), true
	case "y":
		return now.AddDate(-n, 0, 0), true
	}
	return time.Time{}, false
}

func printUsageReport(modelAgg map[string]*usageModelStats, sessions []usageSessionStats, since, until time.Time) error {
	models := make([]*usageModelStats, 0, len(modelAgg))
	for _, s := range modelAgg {
		models = append(models, s)
	}
	// Sort by total tokens desc — busiest model first.
	sort.Slice(models, func(i, j int) bool {
		return (models[i].InputTokens + models[i].OutputTokens) > (models[j].InputTokens + models[j].OutputTokens)
	})

	if usageJSONFlag {
		return emitJSON(models, sessions, since, until)
	}
	emitTable(models, sessions, since, until)
	return nil
}

func emitJSON(models []*usageModelStats, sessions []usageSessionStats, since, until time.Time) error {
	report := map[string]any{
		"models": models,
	}
	if !since.IsZero() {
		report["since"] = since.UTC().Format(time.RFC3339)
	}
	if !until.IsZero() {
		report["until"] = until.UTC().Format(time.RFC3339)
	}
	if usageBySession {
		// Sort sessions by token volume desc.
		sort.Slice(sessions, func(i, j int) bool {
			return (sessions[i].InputTokens + sessions[i].OutputTokens) > (sessions[j].InputTokens + sessions[j].OutputTokens)
		})
		report["sessions"] = sessions
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func emitTable(models []*usageModelStats, sessions []usageSessionStats, since, until time.Time) {
	if !since.IsZero() || !until.IsZero() {
		fmt.Print("Window: ")
		if !since.IsZero() {
			fmt.Print(since.UTC().Format(time.RFC3339))
		} else {
			fmt.Print("(start)")
		}
		fmt.Print(" → ")
		if !until.IsZero() {
			fmt.Print(until.UTC().Format(time.RFC3339))
		} else {
			fmt.Print("(now)")
		}
		fmt.Println()
		fmt.Println()
	}

	if len(models) == 0 {
		fmt.Println("No turns recorded in this window.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tTURNS\tINPUT\tOUTPUT\tTOTAL\tCACHE-HIT\tCOST-USD\tFIRST\tLAST")
	var totalIn, totalOut int64
	var totalCost float64
	var totalTurns, totalCache int
	for _, m := range models {
		total := m.InputTokens + m.OutputTokens
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%d\t$%.4f\t%s\t%s\n",
			m.Model, m.Turns,
			humanizeUsageInt(m.InputTokens), humanizeUsageInt(m.OutputTokens), humanizeUsageInt(total),
			m.CacheHits, m.CostUSD,
			m.First.UTC().Format("2006-01-02"), m.Last.UTC().Format("2006-01-02"))
		totalIn += m.InputTokens
		totalOut += m.OutputTokens
		totalCost += m.CostUSD
		totalTurns += m.Turns
		totalCache += m.CacheHits
	}
	if len(models) > 1 {
		fmt.Fprintf(w, "TOTAL\t%d\t%s\t%s\t%s\t%d\t$%.4f\t\t\n",
			totalTurns,
			humanizeUsageInt(totalIn), humanizeUsageInt(totalOut), humanizeUsageInt(totalIn+totalOut),
			totalCache, totalCost)
	}
	_ = w.Flush()

	if usageBySession && len(sessions) > 0 {
		fmt.Println()
		sort.Slice(sessions, func(i, j int) bool {
			return (sessions[i].InputTokens + sessions[i].OutputTokens) > (sessions[j].InputTokens + sessions[j].OutputTokens)
		})
		ws := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(ws, "SESSION\tREPO\tTURNS\tINPUT\tOUTPUT\tTOTAL\tCOST-USD\tMODELS")
		for _, s := range sessions {
			total := s.InputTokens + s.OutputTokens
			fmt.Fprintf(ws, "%s\t%s\t%d\t%s\t%s\t%s\t$%.4f\t%s\n",
				s.SessionID, s.Repo, s.Turns,
				humanizeUsageInt(s.InputTokens), humanizeUsageInt(s.OutputTokens), humanizeUsageInt(total),
				s.CostUSD, strings.Join(s.Models, ","))
		}
		_ = ws.Flush()
	}
}

// humanizeUsageInt formats with thousand separators (and K/M suffixes
// past 10K) so token counts are readable at a glance.
func humanizeUsageInt(n int64) string {
	if n < 10_000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	if n < 1_000_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%.2fG", float64(n)/1_000_000_000)
}
