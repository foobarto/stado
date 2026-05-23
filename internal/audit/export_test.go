package audit

import (
	"strings"
	"testing"
)

// Codex #143 round 2 defense: parseMessage must NOT treat indented
// `Key: Value` lines (compaction-summary content) as trailers.
// Pre-fix parseMessage TrimSpace'd the key, so `  Tool: bash` in
// a summary parsed as a real Tool trailer overwriting attribution
// under last-write-wins. The fix scans the trailer block from the
// end, accepting only unindented lines whose key matches the
// git-trailer grammar.
func TestParseMessage_IndentedSummaryLinesAreNotTrailers(t *testing.T) {
	msg := "compaction: turns 1..5\n\n" +
		"  Wrote a thing.\n" +
		"  Tool: bash\n" + // attacker's summary-injection attempt
		"  Also did other thing.\n" +
		"\n" +
		"Compaction-From-Turn: 1\n" +
		"Compaction-To-Turn: 5\n" +
		"Tool: real-tool\n"

	title, trailers := parseMessage(msg)
	if title != "compaction: turns 1..5" {
		t.Errorf("title = %q", title)
	}
	if trailers["Tool"] != "real-tool" {
		t.Errorf("Tool trailer should be the real value 'real-tool'; got %q", trailers["Tool"])
	}
	if trailers["Compaction-From-Turn"] != "1" || trailers["Compaction-To-Turn"] != "5" {
		t.Errorf("real trailers missing: %v", trailers)
	}
}

// parseMessage's trailer block is the LAST contiguous run of
// well-formed trailer lines. A `K: V` shaped line earlier in the
// body — even if unindented — should NOT be treated as a trailer
// (the trailer block ends at the first non-trailer line, scanning
// backward).
func TestParseMessage_TrailerBlockIsFinalContiguousRun(t *testing.T) {
	msg := "title\n\n" +
		"Email me at: test@example.com\n" + // colon-bearing prose
		"More body text.\n" +
		"\n" +
		"Tool: real\n" +
		"Turn: 1\n"

	_, trailers := parseMessage(msg)
	// Inline prose with `: ` should NOT become a trailer keyed "Email me at".
	if _, ok := trailers["Email me at"]; ok {
		t.Errorf("inline-prose `K: V` text leaked into trailers: %v", trailers)
	}
	// Real trailers present.
	if trailers["Tool"] != "real" || trailers["Turn"] != "1" {
		t.Errorf("real trailers missing: %v", trailers)
	}
}

// A message with no body still parses — the LAST contiguous trailer
// block extends from after the title-blank to the end.
func TestParseMessage_NoBodyJustTrailers(t *testing.T) {
	msg := "title\n\n" +
		"Tool: bash\n" +
		"Turn: 7\n"

	title, trailers := parseMessage(msg)
	if title != "title" {
		t.Errorf("title = %q", title)
	}
	if len(trailers) != 2 || trailers["Tool"] != "bash" || trailers["Turn"] != "7" {
		t.Errorf("trailers = %v", trailers)
	}
}

// A trailing Signature line is skipped (verifiers don't want the
// signature itself to surface as a trailer in audit JSON).
func TestParseMessage_SignatureLineSkipped(t *testing.T) {
	msg := "title\n\n" +
		"Tool: bash\n" +
		"Signature: ed25519:abcdef==\n"

	_, trailers := parseMessage(msg)
	if _, ok := trailers["Signature"]; ok {
		t.Errorf("Signature should not appear in trailers; got %v", trailers)
	}
	if trailers["Tool"] != "bash" {
		t.Errorf("Tool trailer missing: %v", trailers)
	}
}

// isTrailerLine grammar checks — keys must start with a letter and
// contain only ASCII alnum / `-` / `_`. Indented or weird-keyed lines
// reject.
func TestIsTrailerLine(t *testing.T) {
	cases := map[string]bool{
		"Tool: bash":                    true,
		"Compaction-From-Turn: 1":       true,
		"snake_case: x":                 true,
		"123-bad-key-first-digit: x":    false, // key must start with letter
		"":                              false,
		"  Tool: bash":                  false, // leading space
		"\tTool: bash":                  false, // leading tab
		"Tool":                          false, // no colon
		"Email me at: test@example.com": false, // space in key
	}
	for line, want := range cases {
		if got := isTrailerLine(line); got != want {
			t.Errorf("isTrailerLine(%q) = %v, want %v", line, got, want)
		}
	}
}

// Defense in depth integration: a malicious tool name like
// "Plugin: evil\nTool: bash" goes through CommitMeta.formatMessage
// (which flattens with cleanTrailerValue) and parses back cleanly.
// This test stays in audit/ alongside the parser because that's
// where the regression would actually surface in audit JSON.
func TestParseMessage_NewlineFoldedTrailerValueDoesNotInject(t *testing.T) {
	msg := "write(foo): added foo\n\n" +
		// Codex #144: pre-fix CommitMeta.formatMessage wrote
		// trailer values verbatim — embedded newlines split into
		// multiple trailer lines. cleanTrailerValue (in
		// state/git/commit_meta.go) now folds them to spaces; the
		// resulting message looks like the line below, with no
		// extra trailer lines:
		"Plugin: evil Tool: bash\n" +
		"Tool: write\n" +
		"Turn: 1\n"

	_, trailers := parseMessage(msg)
	// Plugin carries the folded payload as a single value.
	if !strings.Contains(trailers["Plugin"], "evil Tool: bash") {
		t.Errorf("Plugin should carry folded value as one string; got %q", trailers["Plugin"])
	}
	// Tool still has its real value.
	if trailers["Tool"] != "write" {
		t.Errorf("Tool trailer = %q, want write", trailers["Tool"])
	}
}
