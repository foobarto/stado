package textutil

import (
	"strings"
	"unicode"
)

// StripControlChars removes terminal control characters from untrusted text.
// Newlines and tabs are removed too — call sites that want layout should
// reinsert explicit separators rather than trusting raw bytes from repos or
// model/tool output.
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
