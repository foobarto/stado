package git

import (
	"fmt"
	"strings"
	"time"
)

// CommitMeta is the structured per-tool-call metadata we record in every
// commit message (both tree and trace refs). Machine-parseable trailers so
// `stado audit export` / SIEM ingestion can reconstruct the call.
//
// See PLAN.md §2.5 for the commit-message format.
type CommitMeta struct {
	Tool       string
	ShortArg   string // small summary used in the title line
	Summary    string // human one-liner (also the title)
	ArgsSHA    string
	ResultSHA  string
	TokensIn   int
	TokensOut  int
	CacheHit   bool
	CostUSD    float64
	Model      string
	DurationMs int64
	Agent      string
	Turn       int
	Error      string
	// Plugin identifies the plugin that initiated this action, for
	// trace commits made on behalf of plugin-triggered LLM invocations,
	// forks, or tool calls. Empty for actions the core agent loop ran
	// directly. Surfaces as a `Plugin:` trailer so `git log` + `stado
	// audit export` can attribute every commit correctly per DESIGN
	// §"Plugin extension points for context management" invariant 3.
	Plugin string

	// preformatted lets callers (e.g. CommitCompaction) pass an
	// already-rendered message through commitOnRef without going
	// through the tool-call-oriented trailer layout below. Empty →
	// formatMessage builds the standard CommitMeta form.
	preformatted string
}

// formatMessage renders a CommitMeta into the structured commit message.
// First line: `<tool>(<short-arg>): <summary>`. Blank line. Trailer block.
// When preformatted is non-empty, it's returned as-is — the caller has
// already produced the final message (compaction, future custom events).
func (c CommitMeta) formatMessage() string {
	if c.preformatted != "" {
		return c.preformatted
	}
	var b strings.Builder
	title := fmt.Sprintf("%s", c.Tool)
	if c.ShortArg != "" {
		title += "(" + c.ShortArg + ")"
	}
	if c.Summary != "" {
		title += ": " + c.Summary
	}
	b.WriteString(title)
	b.WriteString("\n\n")

	trailers := []struct{ k, v string }{
		{"Tool", c.Tool},
		{"Args-SHA", c.ArgsSHA},
		{"Result-SHA", c.ResultSHA},
		{"Tokens-In", fmt.Sprintf("%d", c.TokensIn)},
		{"Tokens-Out", fmt.Sprintf("%d", c.TokensOut)},
		{"Cache-Hit", boolStr(c.CacheHit)},
		{"Cost-USD", fmt.Sprintf("%.4f", c.CostUSD)},
		{"Model", c.Model},
		{"Duration-Ms", fmt.Sprintf("%d", c.DurationMs)},
		{"Agent", c.Agent},
		{"Turn", fmt.Sprintf("%d", c.Turn)},
	}
	if c.Error != "" {
		trailers = append(trailers, struct{ k, v string }{"Error", c.Error})
	}
	if c.Plugin != "" {
		trailers = append(trailers, struct{ k, v string }{"Plugin", c.Plugin})
	}
	for _, t := range trailers {
		if t.v == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", t.k, t.v)
	}
	return b.String()
}

// CompactionMeta is the payload for a user-accepted /compact event.
// Kept separate from CommitMeta because compaction commits carry
// summary-prose metadata rather than tool-call telemetry — different
// audience (humans reading `git log`), different trailers.
type CompactionMeta struct {
	Title      string // short single-line title for the commit subject
	Summary    string // full summary body
	FromTurn   int    // first turn included in the compaction (0 = session start)
	ToTurn     int    // last turn included
	TurnsTotal int    // number of turns collapsed (for audit)
	ByAuthor   string // who/what ran the compaction (usually the session's bot identity)
	RawLogSHA  string // digest of conversation.jsonl before the compaction event
}

// formatCompactionMessage renders CompactionMeta into the structured
// commit message format shared across tree + trace refs. First line is
// the subject; body is the summary; trailers pin the turn range and
// audit timestamp.
func (c CompactionMeta) formatMessage(ts time.Time) string {
	var b strings.Builder
	title := c.Title
	if title == "" {
		title = fmt.Sprintf("compaction: turns %d..%d", c.FromTurn, c.ToTurn)
	}
	b.WriteString("Compaction: ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if c.Summary != "" {
		b.WriteString(strings.TrimSpace(c.Summary))
		b.WriteString("\n\n")
	}
	trailers := []struct{ k, v string }{
		{"Compaction-From-Turn", fmt.Sprintf("%d", c.FromTurn)},
		{"Compaction-To-Turn", fmt.Sprintf("%d", c.ToTurn)},
		{"Compaction-Turns-Total", fmt.Sprintf("%d", c.TurnsTotal)},
		{"Compaction-At", ts.UTC().Format(time.RFC3339)},
	}
	if c.ByAuthor != "" {
		trailers = append(trailers, struct{ k, v string }{"Compaction-By", c.ByAuthor})
	}
	if c.RawLogSHA != "" {
		trailers = append(trailers, struct{ k, v string }{"Compaction-Raw-Log-SHA", c.RawLogSHA})
	}
	for _, t := range trailers {
		if t.v == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", t.k, t.v)
	}
	return b.String()
}

// preformattedMeta wraps a fully-rendered commit message as a
// CommitMeta that passes it through unchanged. Used when the caller
// has its own formatter (e.g. CompactionMeta.formatMessage) and
// doesn't want CommitMeta's tool-call-specific layout.
func preformattedMeta(msg string) CommitMeta {
	return CommitMeta{preformatted: msg}
}

// boolStr prints true/false rather than "1"/"0" to match PLAN.md's trailer.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
