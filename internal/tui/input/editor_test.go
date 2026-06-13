package input

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/tui/keys"
)

func TestEditorCapsPastedInputBytes(t *testing.T) {
	e := New(keys.NewRegistry())

	_, _ = e.Update(tea.KeyPressMsg{
		Text: strings.Repeat("x", MaxValueBytes+128),
	})

	if got := len(e.Value()); got != MaxValueBytes {
		t.Fatalf("input length = %d, want %d", got, MaxValueBytes)
	}

	_, _ = e.Update(tea.KeyPressMsg{Text: "tail"})
	if got := len(e.Value()); got != MaxValueBytes {
		t.Fatalf("input length after extra paste = %d, want %d", got, MaxValueBytes)
	}
}

func TestEditorCapsPastedInputAtRuneBoundary(t *testing.T) {
	e := New(keys.NewRegistry())
	e.SetValue(strings.Repeat("x", MaxValueBytes-1))

	_, _ = e.Update(tea.KeyPressMsg{Text: "é"})

	if got := len(e.Value()); got != MaxValueBytes-1 {
		t.Fatalf("input length = %d, want %d", got, MaxValueBytes-1)
	}
	if !utf8.ValidString(e.Value()) {
		t.Fatalf("input is not valid UTF-8")
	}
}

func TestEditorSetValueCapsAtRuneBoundary(t *testing.T) {
	e := New(keys.NewRegistry())

	e.SetValue(strings.Repeat("x", MaxValueBytes-1) + "é")

	if got := len(e.Value()); got != MaxValueBytes-1 {
		t.Fatalf("input length = %d, want %d", got, MaxValueBytes-1)
	}
	if !utf8.ValidString(e.Value()) {
		t.Fatalf("input is not valid UTF-8")
	}
}

// P2.6: the placeholder hint is the only always-visible cue about what
// Enter does. It documented "Enter to send, Shift+Enter for new line"
// but never mentioned that plain Enter WHILE A TURN STREAMS steers the
// running turn (#16) — so the steer behavior was undiscoverable from the
// input box. Enrich the hint to surface steering.
func TestEditorPlaceholderHintsSteer(t *testing.T) {
	e := New(keys.NewRegistry())
	if !strings.Contains(strings.ToLower(e.Model.Placeholder), "steer") {
		t.Errorf("input placeholder should hint the Enter-while-busy steer; got %q", e.Model.Placeholder)
	}
}

// TestCursorOffsetByteAlignedWithMultibyte: CursorOffset must return a
// BYTE offset into Value() so callers that byte-slice the buffer (the
// @-mention file picker) don't corrupt multibyte text. "café " is 6
// bytes / 5 runes; with the cursor at end the offset must be 6, not 5.
func TestCursorOffsetByteAlignedWithMultibyte(t *testing.T) {
	e := New(keys.NewRegistry())
	e.SetValue("café ") // SetValue puts cursor at end
	got := e.CursorOffset()
	want := len("café ") // 6 bytes
	if got != want {
		t.Fatalf("CursorOffset() = %d, want %d (byte offset, not rune count)", got, want)
	}
}

// TestCursorOffsetByteAlignedMultiline: byte offset across logical lines
// with a multibyte char on the second line.
func TestCursorOffsetByteAlignedMultiline(t *testing.T) {
	e := New(keys.NewRegistry())
	e.SetValue("ab\ncafé") // cursor at end; "ab\n" = 3 bytes, "café" = 5 bytes -> 8
	got := e.CursorOffset()
	want := len("ab\ncafé")
	if got != want {
		t.Fatalf("CursorOffset() = %d, want %d", got, want)
	}
}

// TestSetValueWithCursorRoundTrip: SetValueWithCursor places the cursor at the
// given byte offset, recoverable via CursorOffset. Exercises single-line,
// multiline, multibyte, and out-of-range offsets — the vim engine relies on
// this round-trip to apply its (buffer, cursor) results.
func TestSetValueWithCursorRoundTrip(t *testing.T) {
	cases := []struct {
		val  string
		off  int
		want int // expected CursorOffset()
	}{
		{"hello", 0, 0},
		{"hello", 3, 3},
		{"hello", 5, 5},
		{"hello", 99, 5},   // clamp past end
		{"hello", -3, 0},   // clamp below zero
		{"ab\ncd", 4, 4},   // second line, col 1
		{"ab\ncd", 3, 3},   // second line start
		{"café", 3, 3},     // before the multibyte é
		{"café", 5, 5},     // after é (end)
		{"ab\ncafé", 6, 6}, // second line, before é
	}
	for _, c := range cases {
		e := New(keys.NewRegistry())
		e.SetValueWithCursor(c.val, c.off)
		if got := e.CursorOffset(); got != c.want {
			t.Errorf("SetValueWithCursor(%q, %d): CursorOffset() = %d, want %d", c.val, c.off, got, c.want)
		}
		if e.Value() != c.val {
			t.Errorf("SetValueWithCursor(%q, %d): Value() = %q, want %q", c.val, c.off, e.Value(), c.val)
		}
	}
}
