package git

import (
	"strings"
	"testing"
	"time"
)

// Codex #144: a trailer value containing a newline used to inject a
// fake `Key: Value` line that the audit parser honored under
// last-write-wins. The cleanTrailerValue helper flattens newlines
// + strips control chars so injection isn't possible at the format
// boundary.
func TestCleanTrailerValue_StripsNewlineInjection(t *testing.T) {
	in := "evil\nTool: bash\nAgent: legit"
	got := cleanTrailerValue(in)
	if strings.Contains(got, "\n") {
		t.Errorf("cleanTrailerValue must drop newlines: %q", got)
	}
	// The original chars still appear (folded onto one line, no
	// longer parsable as a separate trailer).
	if !strings.Contains(got, "Tool: bash") {
		t.Errorf("original chars should still appear, just folded; got %q", got)
	}
}

// C0 / DEL / C1 control runes stripped silently — none have
// legitimate meaning in a trailer value, and several can drive
// terminal escape injection if the trailer is later emitted to
// stderr.
func TestCleanTrailerValue_StripsControlChars(t *testing.T) {
	in := "before\x1bdanger\x07after\x7ftail"
	got := cleanTrailerValue(in)
	want := "beforedangeraftertail"
	if got != want {
		t.Errorf("cleanTrailerValue strip: got %q, want %q", got, want)
	}
}

// Plain printable values + unicode pass through unchanged.
func TestCleanTrailerValue_PassthroughCases(t *testing.T) {
	cases := []string{
		"",
		"simple-value",
		"value with spaces",
		"unicode: éñ漢",
		"path: /tmp/x.go",
		"hash:abcdef1234567890",
	}
	for _, in := range cases {
		if got := cleanTrailerValue(in); got != in {
			t.Errorf("cleanTrailerValue(%q) should pass unchanged; got %q", in, got)
		}
	}
}

// cleanTrailerKey enforces the ASCII alnum / -/_ grammar — a key
// containing a colon, newline, or other punctuation can't slip
// through and break the `K: V\n` line shape.
func TestCleanTrailerKey_StripsInjectionChars(t *testing.T) {
	cases := map[string]string{
		"Tool":            "Tool",
		"Compaction-From": "Compaction-From",
		"snake_case":      "snake_case",
		"bad: key":        "badkey",
		"bad\nkey":        "badkey",
		"bad\x1bkey":      "badkey",
	}
	for in, want := range cases {
		if got := cleanTrailerKey(in); got != want {
			t.Errorf("cleanTrailerKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Integration: format a CommitMeta whose Plugin field is the Codex
// #144 attacker payload — the resulting message must NOT contain
// fake `Tool` or `Agent` trailer lines that overwrite the real
// values under audit/export.go's last-write-wins semantics.
func TestCommitMeta_FormatMessage_DefendsAgainstTrailerInjection(t *testing.T) {
	m := CommitMeta{
		Tool:    "write",
		Summary: "added foo",
		Plugin:  "evil\nTool: bash\nAgent: forged-agent",
		Agent:   "real-agent",
		Turn:    1,
	}
	msg := m.formatMessage()

	// Real trailers must still be present with their real values.
	if !strings.Contains(msg, "Tool: write\n") {
		t.Errorf("real Tool trailer missing: %q", msg)
	}
	if !strings.Contains(msg, "Agent: real-agent\n") {
		t.Errorf("real Agent trailer missing: %q", msg)
	}
	// Injection must NOT have produced STANDALONE trailer lines
	// (start-of-line "K: V\n"). Substring match alone would catch
	// the safe folded Plugin value where the injection payload now
	// lives inline ("Plugin: evil Tool: bash Agent: forged-agent\n");
	// the load-bearing assertion is the line-prefix shape that
	// audit/export.go's parseMessage would treat as a trailer.
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "Tool: bash") {
			t.Errorf("trailer injection produced a standalone Tool line: %q", line)
		}
		if strings.HasPrefix(line, "Agent: forged-agent") {
			t.Errorf("trailer injection produced a standalone Agent line: %q", line)
		}
	}
}

// Codex #143: a compaction summary containing `"Tool: bash"` used to
// inject a fake trailer because the summary was written verbatim
// before the trailer block. Each summary line is now two-space
// indented; audit/export.go's parseMessage skips indented lines (the
// trailer regex requires bare-line start).
func TestCompactionMeta_FormatMessage_DefendsAgainstSummaryTrailerInjection(t *testing.T) {
	m := CompactionMeta{
		Title:      "test compaction",
		Summary:    "Wrote a thing.\nTool: bash\nAlso did the other thing.",
		FromTurn:   1,
		ToTurn:     5,
		TurnsTotal: 5,
	}
	msg := m.formatMessage(time.Time{}) // zero ts; not relevant to this test

	// The summary content should still be present (just indented).
	if !strings.Contains(msg, "Tool: bash") {
		t.Errorf("summary content should survive (just indented): %q", msg)
	}
	// But no unindented `Tool: bash` line — each summary line is
	// two-space indented so the trailer parser skips it.
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "Tool: bash") {
			t.Errorf("summary-line injection produced an unindented trailer-shape line: %q", line)
		}
	}
}
