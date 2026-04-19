package budget

import (
	"strings"
	"testing"
)

// TestTruncateBytesUnderBudget asserts no-op when input fits.
func TestTruncateBytesUnderBudget(t *testing.T) {
	s := "hello world\n"
	got := TruncateBytes(s, 1024, "")
	if got != s {
		t.Fatalf("unexpected change: %q", got)
	}
}

// TestTruncateBytesOverBudget asserts head is kept and marker appended.
func TestTruncateBytesOverBudget(t *testing.T) {
	s := strings.Repeat("x", 2000)
	got := TruncateBytes(s, 500, "call with range=... for more")
	if !strings.HasPrefix(got, strings.Repeat("x", 400)) {
		t.Errorf("head not preserved: %q", got[:40])
	}
	if !strings.Contains(got, "[truncated:") {
		t.Errorf("marker missing: %q", got)
	}
	if !strings.Contains(got, "call with range=... for more") {
		t.Errorf("hint missing: %q", got)
	}
	if !strings.Contains(got, "of 2000 bytes elided") {
		t.Errorf("full-size not surfaced: %q", got)
	}
}

// TestTruncateBytesSnapsToNewline asserts we don't cut mid-line when a
// newline sits close to the cap.
func TestTruncateBytesSnapsToNewline(t *testing.T) {
	// 900 bytes of "x" + newline, then more "y"s. Cap at 1000 — the
	// newline at byte 900 is within the 256-byte look-back window, so
	// the head should end at the newline, not byte 1000.
	s := strings.Repeat("x", 900) + "\n" + strings.Repeat("y", 500)
	got := TruncateBytes(s, 1000, "")
	headEnd := strings.Index(got, "[truncated:")
	if headEnd < 0 {
		t.Fatalf("marker missing: %q", got)
	}
	head := got[:headEnd]
	if !strings.HasSuffix(head, "\n") {
		t.Errorf("expected head to end at newline, got suffix: %q",
			head[max(0, len(head)-20):])
	}
}

// TestTruncateLinesUnderBudget no-ops when within bounds.
func TestTruncateLinesUnderBudget(t *testing.T) {
	s := "a\nb\nc\n"
	if got := TruncateLines(s, 10, ""); got != s {
		t.Fatalf("unexpected change: %q", got)
	}
}

// TestTruncateLinesOverBudget keeps first maxLines lines + marker.
func TestTruncateLinesOverBudget(t *testing.T) {
	var lines []string
	for i := 0; i < 150; i++ {
		lines = append(lines, "match")
	}
	s := strings.Join(lines, "\n")
	got := TruncateLines(s, 100, "narrow pattern")
	out := strings.Split(got, "\n")
	if len(out) != 101 { // 100 matches + 1 marker
		t.Fatalf("expected 101 lines, got %d", len(out))
	}
	if !strings.Contains(out[100], "100 of 150") {
		t.Errorf("marker wrong: %q", out[100])
	}
	if !strings.Contains(out[100], "narrow pattern") {
		t.Errorf("hint missing from marker: %q", out[100])
	}
}

// TestTruncateBashHeadAndTail covers the split-elision shape unique to
// bash output.
func TestTruncateBashHeadAndTail(t *testing.T) {
	head := strings.Repeat("H", 1000)
	middle := strings.Repeat("M", 5000)
	tail := strings.Repeat("T", 1000)
	s := head + middle + tail
	got := TruncateBashOutput(s, 1500)
	if !strings.HasPrefix(got, "H") {
		t.Errorf("head not retained: %q", got[:20])
	}
	if !strings.HasSuffix(got, "T") {
		t.Errorf("tail not retained: %q", got[len(got)-20:])
	}
	if !strings.Contains(got, "bytes elided from the middle") {
		t.Errorf("middle-elide marker missing: %q", got)
	}
}

// TestTruncateBashUnderBudget no-ops when within cap.
func TestTruncateBashUnderBudget(t *testing.T) {
	s := "short output"
	if got := TruncateBashOutput(s, 1000); got != s {
		t.Fatalf("unexpected change: %q", got)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
