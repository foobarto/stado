package textutil

import "testing"

func TestStripControlChars_RemovesEscapeSequences(t *testing.T) {
	got := StripControlChars("ok\x1b]52;clip\x07still\nbad\t")
	if got != "ok]52;clipstillbad" {
		t.Fatalf("StripControlChars = %q", got)
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
