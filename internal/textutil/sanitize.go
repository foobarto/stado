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
