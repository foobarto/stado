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
