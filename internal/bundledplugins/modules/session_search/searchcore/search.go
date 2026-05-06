// Package searchcore is the host-arch-buildable search engine for
// the session_search plugin. The wasm main package imports this
// package and dispatches Run() over the JSON history pulled from
// stado_session_read. Lives in its own package so `go build ./...`
// doesn't fail on the wasip1-only main.go.
package searchcore

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultMaxResults  = 50
	maxAllowedResults  = 1000
	defaultSnippetLen  = 80
	maxAllowedSnippet  = 400
)

// HistoryMessage matches the JSON shape the session:read "history"
// field returns: one object per turn message.
type HistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Args is the JSON args plugin callers pass.
type Args struct {
	Query           string   `json:"query"`
	IsRegex         bool     `json:"is_regex,omitempty"`
	CaseSensitive   bool     `json:"case_sensitive,omitempty"`
	Roles           []string `json:"roles,omitempty"`
	MaxResults      int      `json:"max_results,omitempty"`
	SnippetChars    int      `json:"snippet_chars,omitempty"`
}

// Match is one hit. Index is the 0-based position in the input
// history array.
type Match struct {
	Index       int    `json:"index"`
	Role        string `json:"role"`
	Snippet     string `json:"snippet"`
	MatchOffset int    `json:"match_offset"`
	MatchLength int    `json:"match_length"`
}

// Result is what the plugin returns to the agent.
type Result struct {
	Matches         []Match `json:"matches"`
	TotalMessages   int     `json:"total_messages"`
	MatchedMessages int     `json:"matched_messages"`
}

// Run is the testable core. Pure function over the history +
// args; no wasm imports. Returns an error only on malformed regex
// or invalid args.
func Run(history []HistoryMessage, args Args) (Result, error) {
	if args.Query == "" {
		return Result{}, errors.New("query is required")
	}
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	if maxResults > maxAllowedResults {
		maxResults = maxAllowedResults
	}
	snippetLen := args.SnippetChars
	if snippetLen <= 0 {
		snippetLen = defaultSnippetLen
	}
	if snippetLen > maxAllowedSnippet {
		snippetLen = maxAllowedSnippet
	}
	roleFilter := NormaliseRoles(args.Roles)

	var re *regexp.Regexp
	if args.IsRegex {
		expr := args.Query
		if !args.CaseSensitive {
			expr = "(?i)" + expr
		}
		r, err := regexp.Compile(expr)
		if err != nil {
			return Result{}, fmt.Errorf("invalid regex: %w", err)
		}
		re = r
	}

	out := Result{TotalMessages: len(history)}
	for i, msg := range history {
		if !RoleAllowed(msg.Role, roleFilter) {
			continue
		}
		offset, length := -1, 0
		if args.IsRegex {
			loc := re.FindStringIndex(msg.Text)
			if loc != nil {
				offset, length = loc[0], loc[1]-loc[0]
			}
		} else {
			needle := args.Query
			haystack := msg.Text
			if !args.CaseSensitive {
				needle = strings.ToLower(needle)
				haystack = strings.ToLower(haystack)
			}
			if idx := strings.Index(haystack, needle); idx >= 0 {
				offset, length = idx, len(needle)
			}
		}
		if offset < 0 {
			continue
		}
		out.MatchedMessages++
		if len(out.Matches) >= maxResults {
			continue
		}
		out.Matches = append(out.Matches, Match{
			Index:       i,
			Role:        msg.Role,
			Snippet:     ExtractSnippet(msg.Text, offset, length, snippetLen),
			MatchOffset: offset,
			MatchLength: length,
		})
	}
	return out, nil
}

// NormaliseRoles lowercases + dedupes the role filter. Empty input
// returns nil → all roles allowed.
func NormaliseRoles(roles []string) map[string]bool {
	if len(roles) == 0 {
		return nil
	}
	set := make(map[string]bool, len(roles))
	for _, r := range roles {
		set[strings.ToLower(strings.TrimSpace(r))] = true
	}
	return set
}

// RoleAllowed reports whether a message role passes the filter.
// nil filter = all roles allowed.
func RoleAllowed(role string, filter map[string]bool) bool {
	if filter == nil {
		return true
	}
	return filter[strings.ToLower(role)]
}

// ExtractSnippet returns up to total chars of text centred on the
// Match. Adds "…" markers when the snippet was truncated at either
// end. The snippet preserves the Match itself in full even if the
// requested total is too small to centre.
func ExtractSnippet(text string, matchStart, matchLen, total int) string {
	if total <= 0 || len(text) == 0 {
		return ""
	}
	if total >= len(text) {
		return text
	}
	// Half the requested window goes on each side of the Match.
	side := (total - matchLen) / 2
	if side < 0 {
		side = 0
	}
	start := matchStart - side
	end := matchStart + matchLen + side
	if start < 0 {
		end -= start
		start = 0
	}
	if end > len(text) {
		shift := end - len(text)
		start -= shift
		end = len(text)
		if start < 0 {
			start = 0
		}
	}
	out := text[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out = out + "…"
	}
	return out
}

