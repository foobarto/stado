package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// StripControlChars removes terminal control characters from untrusted text.
// Newlines and tabs are removed too — call sites that want layout should
// reinsert explicit separators rather than trusting raw bytes from repos or
// model/tool output.
//
// Use this for single-line surfaces — identifiers, names, manifest fields,
// table cells. For prose / multi-line output, use [SanitizeForTerminal]
// which preserves `\n`, `\t`, `\r` while still stripping ESC / CSI / OSC /
// C1 control bytes (the actual escape-injection vectors).
func StripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeForTerminal strips terminal control characters from untrusted
// prose while preserving `\n`, `\t`, and `\r` so legitimate multi-line
// text (assistant deltas, plugin print/progress, manifest descriptions)
// isn't mangled.
//
// What it strips: every other rune for which [unicode.IsControl] is true,
// which covers C0 (U+0000–U+001F minus the three we keep), DEL (U+007F),
// and C1 (U+0080–U+009F). That set includes ESC (0x1B), the CSI / OSC /
// DCS / APC / PM introducers, and BEL — the bytes a hostile model, tool,
// plugin, or manifest can use for OSC 52 (clipboard hijack), OSC 8
// (clickable hyperlink injection), CSI cursor manipulation, title-bar
// rewrite, etc.
//
// What it keeps: `\t`, `\n`, `\r` (Unicode classifies all three as Cc
// control but they're the layout primitives every TUI render path
// depends on). Operators see legitimate formatting; the escape vectors
// are gone.
//
// Use this for text that's expected to be multi-line or to contain
// legitimate whitespace formatting. For single-line identifiers (model
// IDs, plugin names, memory IDs), use [StripControlChars] — anything
// claiming to be an identifier that contains a newline is almost
// certainly an injection attempt and should be flattened.
func SanitizeForTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func HasControlChars(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// TrimLastRune removes one complete rune from the end of s.
func TrimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

// TruncateRunes caps s at n display runes, appending "…" when it had to
// drop any. Unlike a raw `s[:n-1]` byte slice it counts runes, never
// splits a multibyte UTF-8 rune, and so never emits a mojibake half-rune
// before the ellipsis. The ellipsis counts toward the budget: the result
// is at most n runes wide.
//
// Use this for any user- or model-influenced text headed to a fixed-width
// terminal surface (session summaries, compaction titles, table cells).
// n <= 0 yields "" and n == 1 yields just "…" (no room for content).
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	// Keep n-1 runes, then the ellipsis, for a total visible width of n.
	keep := n - 1
	count := 0
	for i := range s {
		if count == keep {
			return s[:i] + "…"
		}
		count++
	}
	// Unreachable given the RuneCount check above, but stay safe.
	return s + "…"
}

// AppendWithinBytes appends as much of addition as fits under maxBytes
// without splitting a UTF-8 rune.
func AppendWithinBytes(current, addition string, maxBytes int) string {
	if maxBytes <= 0 || len(current) >= maxBytes || addition == "" {
		return current
	}
	room := maxBytes - len(current)
	if len(addition) <= room {
		return current + addition
	}
	end := 0
	for i, r := range addition {
		next := i + len(string(r))
		if next > room {
			break
		}
		end = next
	}
	if end == 0 {
		return current
	}
	return current + addition[:end]
}
