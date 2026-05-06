package searchcore

import (
	"strings"
	"testing"
)

var sampleHistory = []HistoryMessage{
	{Role: "user", Text: "Can we discuss authentication for the API?"},
	{Role: "assistant", Text: "Sure — what auth scheme are you using? OAuth, JWT, basic auth?"},
	{Role: "user", Text: "We're on JWT today but considering OAuth migration."},
	{Role: "tool", Text: "ran: ./check-jwt --verify-tokens"},
	{Role: "assistant", Text: "OAuth gives you delegated authorization out of the box."},
}

func TestRunSearch_SubstringDefault(t *testing.T) {
	res, err := Run(sampleHistory, Args{Query: "OAuth"})
	if err != nil {
		t.Fatal(err)
	}
	if res.MatchedMessages != 3 {
		t.Errorf("matched_messages = %d, want 3", res.MatchedMessages)
	}
	if res.TotalMessages != 5 {
		t.Errorf("total_messages = %d, want 5", res.TotalMessages)
	}
	// Default is case-insensitive — "auth" inside "authentication" / "auth?"
	res, err = Run(sampleHistory, Args{Query: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if res.MatchedMessages == 0 {
		t.Error("case-insensitive default should Match")
	}
}

func TestRunSearch_CaseSensitive(t *testing.T) {
	res, err := Run(sampleHistory, Args{Query: "OAUTH", CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.MatchedMessages != 0 {
		t.Errorf("OAUTH case-sensitive: want 0 matches, got %d", res.MatchedMessages)
	}
}

func TestRunSearch_Regex(t *testing.T) {
	// Case-sensitive regex on uppercase JWT — only the assistant
	// + user messages Match; the tool message has lowercase "jwt".
	res, err := Run(sampleHistory, Args{
		Query: `\bJWT\b`, IsRegex: true, CaseSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.MatchedMessages != 2 {
		t.Errorf("regex \\bJWT\\b case-sensitive: want 2 matches, got %d", res.MatchedMessages)
	}
}

func TestRunSearch_RegexInvalid(t *testing.T) {
	_, err := Run(sampleHistory, Args{Query: `[invalid`, IsRegex: true})
	if err == nil {
		t.Error("expected error for malformed regex")
	}
}

func TestRunSearch_RoleFilter(t *testing.T) {
	res, err := Run(sampleHistory, Args{Query: "auth", Roles: []string{"assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Matches {
		if m.Role != "assistant" {
			t.Errorf("role filter failed: got role=%q", m.Role)
		}
	}
	if res.MatchedMessages == 0 {
		t.Error("expected at least one assistant Match for 'auth'")
	}
}

func TestRunSearch_MaxResults(t *testing.T) {
	// Build a history with many matches, ensure cap is enforced.
	hist := make([]HistoryMessage, 100)
	for i := range hist {
		hist[i] = HistoryMessage{Role: "user", Text: "Match me"}
	}
	res, err := Run(hist, Args{Query: "Match", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 5 {
		t.Errorf("max_results=5: got %d matches", len(res.Matches))
	}
	if res.MatchedMessages != 100 {
		t.Errorf("matched_messages should still report all hits: got %d", res.MatchedMessages)
	}
}

func TestRunSearch_MaxResultsCap(t *testing.T) {
	// max_results > maxAllowedResults gets clamped silently.
	hist := make([]HistoryMessage, 2)
	hist[0] = HistoryMessage{Role: "user", Text: "x"}
	hist[1] = HistoryMessage{Role: "user", Text: "x"}
	res, err := Run(hist, Args{Query: "x", MaxResults: 99999})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 2 {
		t.Errorf("expected 2 matches, got %d", len(res.Matches))
	}
}

func TestRunSearch_EmptyQuery(t *testing.T) {
	_, err := Run(sampleHistory, Args{})
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestExtractSnippet(t *testing.T) {
	long := strings.Repeat("abc ", 100) + "TARGET" + strings.Repeat(" xyz", 100)
	matchStart := strings.Index(long, "TARGET")
	got := ExtractSnippet(long, matchStart, len("TARGET"), 40)
	if !strings.Contains(got, "TARGET") {
		t.Errorf("snippet should contain Match: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("snippet should be ellipsis-wrapped on both sides: %q", got)
	}
}

func TestExtractSnippet_ShortText(t *testing.T) {
	got := ExtractSnippet("hello world", 6, 5, 80)
	if got != "hello world" {
		t.Errorf("short text shouldn't be truncated: got %q", got)
	}
}

func TestExtractSnippet_MatchAtStart(t *testing.T) {
	long := "PREFIX" + strings.Repeat(" abc", 100)
	got := ExtractSnippet(long, 0, 6, 40)
	if !strings.HasPrefix(got, "PREFIX") {
		t.Errorf("Match-at-start snippet should keep the Match anchored at start: %q", got)
	}
	if strings.HasPrefix(got, "…") {
		t.Errorf("Match-at-start should not have leading ellipsis: %q", got)
	}
}

func TestNormaliseRoles(t *testing.T) {
	got := NormaliseRoles([]string{"User", "Assistant", "user"})
	if !got["user"] || !got["assistant"] {
		t.Errorf("NormaliseRoles: got %v", got)
	}
	if NormaliseRoles(nil) != nil {
		t.Error("nil input should return nil filter (= all roles allowed)")
	}
}

func TestRoleAllowed_NilFilterAllowsAll(t *testing.T) {
	if !RoleAllowed("anything", nil) {
		t.Error("nil filter should allow any role")
	}
}
