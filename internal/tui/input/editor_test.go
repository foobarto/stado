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
