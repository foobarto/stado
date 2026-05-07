// Package sessionstats walks one session's git-native trace ref and
// returns a per-session token / cost / tool summary. Distinct from
// `stado stats` (cmd/stado/stats.go) which aggregates across sessions
// with filter knobs — this package is a focused single-session view
// the TUI prints at quit time so the operator sees what they spent.
//
// Source of truth is the same as `stado stats`: commit trailers
// stado writes per tool call (CommitMeta in internal/state/git).
// Works offline, works airgap, works whether or not an OTel collector
// was running.
package sessionstats

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// Summary is the single-session aggregation produced by Walk.
type Summary struct {
	SessionID  string
	TotalCalls int
	TokensIn   int
	TokensOut  int
	CostUSD    float64
	DurationMs int64
	ByModel    map[string]ModelStats
	ByTool     map[string]ToolStats
	// FirstAt is the author-time of the earliest tool-call commit.
	FirstAt time.Time
	// LastAt is the author-time of the most recent tool-call commit.
	LastAt time.Time
}

// ModelStats is per-model rollup.
type ModelStats struct {
	Calls     int
	TokensIn  int
	TokensOut int
	CostUSD   float64
}

// ToolStats is per-tool rollup. DurationMs sums the wall-clock time
// reported by the tool itself (stado_progress / Duration-Ms trailer).
type ToolStats struct {
	Calls      int
	DurationMs int64
}

// Empty reports whether the session produced no tool-call commits.
func (s *Summary) Empty() bool { return s == nil || s.TotalCalls == 0 }

// Walk follows the session's trace ref backwards, parses each tool-
// call commit's trailers, and rolls them up. Missing trace ref =
// empty Summary, no error. Session ids that have never produced a
// tool call return Empty()==true.
func Walk(sc *stadogit.Sidecar, sessionID string) (*Summary, error) {
	out := &Summary{
		SessionID: sessionID,
		ByModel:   map[string]ModelStats{},
		ByTool:    map[string]ToolStats{},
	}
	if sc == nil || sessionID == "" {
		return out, nil
	}
	head, err := sc.ResolveRef(stadogit.TraceRef(sessionID))
	if err != nil || head.IsZero() {
		return out, nil
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
			return out, err
		}
		absorbCommit(out, c)
		if len(c.ParentHashes) == 0 {
			break
		}
		cur = c.ParentHashes[0]
	}
	return out, nil
}

func absorbCommit(s *Summary, c *object.Commit) {
	_, trailers := parseCommitMessage(c.Message)
	tool := trailers["Tool"]
	if tool == "" {
		return // not a tool-call trace commit
	}

	in := atoiSafe(trailers["Tokens-In"])
	out := atoiSafe(trailers["Tokens-Out"])
	cost := atofSafe(trailers["Cost-USD"])
	ms := atoi64Safe(trailers["Duration-Ms"])
	model := trailers["Model"]

	s.TotalCalls++
	s.TokensIn += in
	s.TokensOut += out
	s.CostUSD += cost
	s.DurationMs += ms
	if s.FirstAt.IsZero() || c.Author.When.Before(s.FirstAt) {
		s.FirstAt = c.Author.When
	}
	if c.Author.When.After(s.LastAt) {
		s.LastAt = c.Author.When
	}

	if model != "" {
		ms := s.ByModel[model]
		ms.Calls++
		ms.TokensIn += in
		ms.TokensOut += out
		ms.CostUSD += cost
		s.ByModel[model] = ms
	}
	ts := s.ByTool[tool]
	ts.Calls++
	ts.DurationMs += atoi64Safe(trailers["Duration-Ms"])
	s.ByTool[tool] = ts
}

// Render writes a compact human-readable summary to w. Header line
// covers totals; per-model + per-tool tables follow when non-empty.
// Sorts model rows by cost desc, tool rows by call count desc.
//
// uptime is the session's wall-clock lifetime from the operator's
// POV — TUI passes time.Since(startedAt). If zero, falls back to
// (LastAt - FirstAt) from the commit timestamps.
func Render(w io.Writer, s *Summary, uptime time.Duration) {
	if s == nil || s.Empty() {
		fmt.Fprintln(w, "stado: no tool calls this session")
		return
	}
	if uptime == 0 && !s.FirstAt.IsZero() {
		uptime = s.LastAt.Sub(s.FirstAt)
	}

	fmt.Fprintln(w, "── session summary ──────────────────────────────────────")
	fmt.Fprintf(w, "  uptime:     %s\n", fmtDuration(uptime))
	fmt.Fprintf(w, "  tool calls: %d\n", s.TotalCalls)
	fmt.Fprintf(w, "  tokens:     %s in / %s out\n", fmtThousands(s.TokensIn), fmtThousands(s.TokensOut))
	fmt.Fprintf(w, "  cost:       %s\n", fmtCostUSD(s.CostUSD))

	if len(s.ByModel) > 0 {
		fmt.Fprintln(w, "  by model:")
		type row struct {
			name  string
			stats ModelStats
		}
		rows := make([]row, 0, len(s.ByModel))
		for n, st := range s.ByModel {
			rows = append(rows, row{n, st})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].stats.CostUSD != rows[j].stats.CostUSD {
				return rows[i].stats.CostUSD > rows[j].stats.CostUSD
			}
			return rows[i].name < rows[j].name
		})
		for _, r := range rows {
			fmt.Fprintf(w, "    %-30s  %4d calls  %s in / %s out  %s\n",
				truncMid(r.name, 30),
				r.stats.Calls,
				fmtThousands(r.stats.TokensIn),
				fmtThousands(r.stats.TokensOut),
				fmtCostUSD(r.stats.CostUSD),
			)
		}
	}
	if len(s.ByTool) > 0 {
		fmt.Fprintln(w, "  by tool:")
		type row struct {
			name  string
			stats ToolStats
		}
		rows := make([]row, 0, len(s.ByTool))
		for n, st := range s.ByTool {
			rows = append(rows, row{n, st})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].stats.Calls != rows[j].stats.Calls {
				return rows[i].stats.Calls > rows[j].stats.Calls
			}
			return rows[i].name < rows[j].name
		})
		for _, r := range rows {
			fmt.Fprintf(w, "    %-30s  %4d calls  %s\n",
				truncMid(r.name, 30),
				r.stats.Calls,
				fmtMs(r.stats.DurationMs),
			)
		}
	}
	fmt.Fprintln(w, "─────────────────────────────────────────────────────────")
}

func fmtDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func fmtCostUSD(c float64) string {
	if c >= 1 {
		return fmt.Sprintf("$%.2f", c)
	}
	if c == 0 {
		return "$0.00"
	}
	return fmt.Sprintf("$%.4f", c)
}

func fmtMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1fm", float64(ms)/60_000)
}

func fmtThousands(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

func truncMid(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max < 6 {
		return s[:max]
	}
	keep := (max - 1) / 2
	return s[:keep] + "…" + s[len(s)-keep:]
}

// parseCommitMessage extracts trailers from a commit body. Mirrors
// the parser in cmd/stado/stats.go without depending on the package
// main implementation. Trailers are key/value pairs after the first
// blank line, separated by ":". The Signature trailer is stripped
// because audit signatures aren't useful here.
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

func atoiSafe(s string) int {
	var n int
	var negative bool
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < '0' || b > '9' {
			if i == 0 && b == '-' {
				negative = true
				continue
			}
			return 0
		}
		n = n*10 + int(b-'0')
	}
	if negative {
		return -n
	}
	return n
}

func atoi64Safe(s string) int64 { return int64(atoiSafe(s)) }

func atofSafe(s string) float64 {
	var out float64
	var seenDot bool
	var frac float64 = 1
	var negative bool
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b == '-' && i == 0:
			negative = true
		case b == '.' && !seenDot:
			seenDot = true
		case b >= '0' && b <= '9':
			if seenDot {
				frac *= 10
				out = out + float64(b-'0')/frac
			} else {
				out = out*10 + float64(b-'0')
			}
		default:
			return 0
		}
	}
	if negative {
		return -out
	}
	return out
}
