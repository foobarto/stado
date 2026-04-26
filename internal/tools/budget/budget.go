// Package budget centralises per-tool output-size budgets and truncation
// helpers from DESIGN §"Tool-output curation".
//
// Leaf package — imports nothing from internal/tools — so the top-level
// tools package and each per-tool implementation under
// internal/tools/<name>/ can both consume it without cycles.
//
// Budgets are stated here in bytes, with a 1-token ≈ 4-bytes English
// heuristic against DESIGN's token-denominated caps. Cheap enough to
// apply on every tool call (no tokenizer round-trip) and accurate
// enough to prevent runaway output. Sanity tests in each tool's _test.go
// pin these against the DESIGN table so drift fires at CI time.
package budget

import (
	"fmt"
	"strings"
)

// Default per-tool budgets.
const (
	// ReadBytes is the soft cap for `read` output. ≈ 4K tokens of text.
	ReadBytes = 16 * 1024
	// WebfetchBytes is the soft cap for `webfetch`. Same ≈ 4K tokens
	// assumption.
	WebfetchBytes = 16 * 1024
	// BashBytes is the combined stdout+stderr cap for `bash`. ≈ 8K tokens.
	BashBytes = 32 * 1024
	// TasksBytes caps JSON returned by the shared task tool. The store
	// itself enforces per-field limits, but list/read responses still need
	// a hard ceiling before they enter model context.
	TasksBytes = 64 * 1024
	// MCPBytes caps text returned by external MCP tools before their output
	// is added to model context.
	MCPBytes = 64 * 1024
	// GrepMatches is the maximum line-matches retained by the in-process
	// `grep` tool. Stateless list-cut; no per-line token math.
	GrepMatches = 100
	// RipgrepMatches mirrors GrepMatches for the external `rg` tool.
	RipgrepMatches = 100
	// GlobEntries is the maximum path entries returned by `glob`.
	GlobEntries = 200
)

// TruncateBytes caps s to maxBytes. When applied, head content is
// preserved and a trailing marker explains what was elided. Matches the
// DESIGN marker vocabulary so every tool's truncation reads the same
// way to the model.
//
// If len(s) <= maxBytes the input is returned unchanged. hint is an
// optional per-tool instruction appended to the marker (e.g.
// "call with start=... for more").
func TruncateBytes(s string, maxBytes int, hint string) string {
	if len(s) <= maxBytes {
		return s
	}
	head := s[:maxBytes]
	// Avoid cutting mid-line when the last newline is close to the cap.
	if i := strings.LastIndexByte(head, '\n'); i > 0 && i > maxBytes-256 {
		head = head[:i]
	}
	marker := fmt.Sprintf("\n[truncated: %d of %d bytes elided",
		len(s)-len(head), len(s))
	if hint != "" {
		marker += " — " + hint
	}
	marker += "]"
	return head + marker
}

// TruncateLines caps the newline-separated entries in s to maxLines. The
// trailing marker line tells the model how many were elided.
func TruncateLines(s string, maxLines int, hint string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	kept := lines[:maxLines]
	marker := fmt.Sprintf("[truncated: %d of %d matches shown",
		maxLines, len(lines))
	if hint != "" {
		marker += " — " + hint
	}
	marker += "]"
	kept = append(kept, marker)
	return strings.Join(kept, "\n")
}

// TruncateBashOutput keeps the head + tail of long combined stdout+stderr
// and elides the middle with a byte-count marker. Bash output has
// signal at both ends (warnings top, error summary bottom); pure head
// truncation drops the most informative part.
func TruncateBashOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const markerMax = 96
	head := (maxBytes - markerMax) * 3 / 5
	tail := maxBytes - markerMax - head
	elided := len(s) - head - tail
	marker := fmt.Sprintf("\n\n[truncated: %d of %d bytes elided from the middle]\n\n",
		elided, len(s))
	return s[:head] + marker + s[len(s)-tail:]
}
