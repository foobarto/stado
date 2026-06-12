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

	// Hook-mutation provenance (spec: hooks-audit-mutation-provenance,
	// WAVE 1 / SHA-only). All four are zero-valued by default and render
	// as trailers ONLY when set — purely additive, no existing
	// v0.62/v0.63 signature is rewritten.
	//
	// OriginalResultSHA + MutatedByHook are set on the SECOND commit of
	// the two-commit mutation model: a post_tool hook rewrote res before
	// audit, so the audited (mutated) Result-SHA hashes the mutated
	// bytes while OriginalResultSHA preserves the pre-mutation result's
	// digest and MutatedByHook attributes the winning hook. The first
	// commit (the original-result provenance entry) carries neither.
	OriginalResultSHA string
	MutatedByHook     string

	// DenyReason + DeniedByHook are set on the trace commit a pre_tool
	// DENY now writes before early-returning (denials were invisible in
	// the audit chain pre-fix). DeniedByHook attributes the vetoing
	// hook; DenyReason is its (model-influenceable) explanation —
	// routed through cleanTrailerValue like every other untrusted value.
	DenyReason   string
	DeniedByHook string

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
	// Hook-mutation provenance (WAVE 1). Each renders only when set; the
	// `if t.v == "" continue` gate + cleanTrailerKey/cleanTrailerValue
	// injection defense below handle hostile (model-influenceable) hook
	// names + deny reasons.
	if c.OriginalResultSHA != "" {
		trailers = append(trailers, struct{ k, v string }{"Original-Result-SHA", c.OriginalResultSHA})
	}
	if c.MutatedByHook != "" {
		trailers = append(trailers, struct{ k, v string }{"Mutated-By-Hook", c.MutatedByHook})
	}
	if c.DenyReason != "" {
		trailers = append(trailers, struct{ k, v string }{"Deny-Reason", c.DenyReason})
	}
	if c.DeniedByHook != "" {
		trailers = append(trailers, struct{ k, v string }{"Denied-By-Hook", c.DeniedByHook})
	}
	for _, t := range trailers {
		if t.v == "" {
			continue
		}
		// Codex #143/#144: strip newlines + control chars so an
		// attacker-controllable value (plugin name, tool error,
		// model-generated content in a Summary) can't inject extra
		// `Key: Value` lines that the audit parser
		// (audit/export.go parseMessage) would honor under
		// last-write-wins.
		fmt.Fprintf(&b, "%s: %s\n", cleanTrailerKey(t.k), cleanTrailerValue(t.v))
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
	if trimmed := strings.TrimSpace(c.Summary); trimmed != "" {
		// Codex #143: indent each summary line with two spaces +
		// pair with the audit/export.go parseMessage tightening
		// (defense in depth — parser is the load-bearing layer that
		// rejects indented `K: V` lines as fake trailers). Git's own
		// trailer parser also skips indented lines, so the indented
		// summary remains human-readable through `git log`.
		// Trim FIRST so a whitespace-only Summary doesn't produce an
		// empty indented blank line.
		for _, line := range strings.Split(trimmed, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteString("\n")
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
		// Codex #143/#144: strip newlines + control chars so an
		// attacker-controllable value (plugin name, tool error,
		// model-generated content in a Summary) can't inject extra
		// `Key: Value` lines that the audit parser
		// (audit/export.go parseMessage) would honor under
		// last-write-wins.
		fmt.Fprintf(&b, "%s: %s\n", cleanTrailerKey(t.k), cleanTrailerValue(t.v))
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

// cleanTrailerValue strips newlines and other control characters from a
// trailer value before it goes into the commit message. Without this,
// an attacker-controllable value (plugin name, tool error string,
// model output that lands in a Summary line) could embed `\n` to
// inject extra `Key: Value` lines that the audit parser
// ([internal/audit/export.go] parseMessage) would treat as real
// trailers, overwriting attribution under last-write-wins semantics.
//
// Codex #143 + #144: pre-fix the formatters used raw `fmt.Fprintf`
// with `%s` for every trailer value. A malicious `Plugin: "evil\nTool:
// bash\nAgent: legit"` produced three trailer lines; a compaction
// summary containing `"Tool: bash"` injected a fake tool trailer.
//
// Replaces newlines with space (preserves trailer-line wrapping
// semantics while eliminating injection); strips other C0/C1
// controls outright (BEL, ESC, DEL, etc.) — none have legitimate
// meaning in a trailer value.
func cleanTrailerValue(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		switch r {
		case '\n', '\r':
			b.WriteByte(' ')
		case '\t':
			b.WriteByte(' ')
		default:
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				continue // strip other C0 + DEL + C1 silently
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanTrailerKey enforces the strict trailer-key grammar so a
// malicious tool name (`Tool: ev:il\nFake: bash`) can't slip a colon
// into the key. Keys are constants in the formatters today, but
// passing them through this helper future-proofs against a future
// caller that derives the key from untrusted data. ASCII alnum,
// hyphen, and underscore only — git-trailer convention.
func cleanTrailerKey(k string) string {
	var b strings.Builder
	b.Grow(len(k))
	for _, r := range k {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
