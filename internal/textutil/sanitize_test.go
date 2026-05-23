package textutil

import "testing"

func TestStripControlChars_RemovesEscapeSequences(t *testing.T) {
	got := StripControlChars("ok\x1b]52;clip\x07still\nbad\t")
	if got != "ok]52;clipstillbad" {
		t.Fatalf("StripControlChars = %q", got)
	}
}

// SanitizeForTerminal keeps the three layout-bearing whitespace runes
// (\n, \t, \r) but strips every other unicode.IsControl rune. The
// load-bearing assertion is that ESC (0x1B), the CSI/OSC introducers,
// BEL (0x07), and DEL (0x7F) are removed — those are the actual
// escape-injection vectors.
func TestSanitizeForTerminal_KeepsWhitespaceStripsEscapes(t *testing.T) {
	in := "first\n\tsecond\r\x1b]52;clip\x07third\x7ffourth"
	got := SanitizeForTerminal(in)
	want := "first\n\tsecond\r]52;clipthirdfourth"
	if got != want {
		t.Fatalf("SanitizeForTerminal = %q, want %q", got, want)
	}
}

// C1 control bytes (U+0080–U+009F) are alternate forms of ESC + ASCII —
// some terminals interpret them. Strip them too.
func TestSanitizeForTerminal_StripsC1Controls(t *testing.T) {
	in := "before\u009bdangerafter"
	got := SanitizeForTerminal(in)
	want := "beforedangerafter"
	if got != want {
		t.Fatalf("SanitizeForTerminal = %q, want %q", got, want)
	}
}

// Empty / pure-whitespace / plain-printable cases shouldn't be touched.
func TestSanitizeForTerminal_PassthroughCases(t *testing.T) {
	cases := []string{
		"",
		"plain ascii",
		"multi\nline\nprose",
		"with\ttabs",
		"unicode: éñ漢",
	}
	for _, in := range cases {
		if got := SanitizeForTerminal(in); got != in {
			t.Errorf("SanitizeForTerminal(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestHasControlChars(t *testing.T) {
	if HasControlChars("plain.txt") {
		t.Fatal("plain text should not report controls")
	}
	if !HasControlChars("bad\x1bname") {
		t.Fatal("control sequence should be detected")
	}
}

func TestTrimLastRune(t *testing.T) {
	if got := TrimLastRune("zaż"); got != "za" {
		t.Fatalf("TrimLastRune = %q, want za", got)
	}
	if got := TrimLastRune(""); got != "" {
		t.Fatalf("TrimLastRune empty = %q, want empty", got)
	}
}

func TestAppendWithinBytesCapsAtRuneBoundary(t *testing.T) {
	if got := AppendWithinBytes("xx", "abc", 4); got != "xxab" {
		t.Fatalf("AppendWithinBytes ASCII = %q, want xxab", got)
	}
	if got := AppendWithinBytes("xx", "é", 3); got != "xx" {
		t.Fatalf("AppendWithinBytes split rune = %q, want xx", got)
	}
	if got := AppendWithinBytes("xx", "é", 4); got != "xxé" {
		t.Fatalf("AppendWithinBytes full rune = %q, want xxé", got)
	}
}
