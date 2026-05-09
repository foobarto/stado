package ptyblock

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// TestMain forces a color profile so lipgloss emits ANSI escapes
// even though the test environment isn't a TTY. Without this, every
// style call returns plain text and the size-based assertions below
// (styled output longer than plain) fail vacuously. The TUI itself
// runs in a real TTY where this happens automatically.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// fixture builds a Screen with the given rune content. Each row is
// one string; cells take fg=7 (default) bg=0 (default) attr=0. Useful
// for asserting layout without colour noise.
func fixture(rows ...string) *pty.Screen {
	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	cells := make([][]pty.Cell, len(rows))
	for y, line := range rows {
		row := make([]pty.Cell, maxCols)
		for x := 0; x < maxCols; x++ {
			var ch rune
			if x < len(line) {
				ch = rune(line[x])
			}
			row[x] = pty.Cell{Char: ch, FG: 7, BG: 0}
		}
		cells[y] = row
	}
	return &pty.Screen{Cols: maxCols, Rows: len(rows), Cells: cells}
}

// TestRender_PlainText: a screen of default-styled glyphs renders to
// row-per-line text. Stripping ANSI escape codes (which lipgloss
// emits regardless) should yield the original characters.
func TestRender_PlainText(t *testing.T) {
	s := fixture(
		"hello",
		"world",
	)
	out := Render(s, nil)
	stripped := stripANSI(out)
	wantLines := []string{"hello", "world"}
	gotLines := strings.Split(stripped, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("got %d lines, want %d (raw=%q stripped=%q)", len(gotLines), len(wantLines), out, stripped)
	}
	for i, want := range wantLines {
		if gotLines[i] != want {
			t.Errorf("line %d = %q, want %q", i, gotLines[i], want)
		}
	}
}

// TestRender_EmptyCellsBecomeSpaces: cells with Char=0 (unwritten)
// render as ASCII space. Locks the column-alignment guarantee.
func TestRender_EmptyCellsBecomeSpaces(t *testing.T) {
	cells := [][]pty.Cell{
		{
			{Char: 'h', FG: 7, BG: 0},
			{Char: 0, FG: 7, BG: 0},
			{Char: 'i', FG: 7, BG: 0},
		},
	}
	s := &pty.Screen{Cols: 3, Rows: 1, Cells: cells}
	out := stripANSI(Render(s, nil))
	if out != "h i" {
		t.Errorf("got %q, want %q", out, "h i")
	}
}

// TestRender_ControlCharsBecomeSpaces: a literal 0x07 (BEL) or 0x1b
// (ESC) in the cell would otherwise execute on the embedding terminal.
// Replacing them with spaces preserves alignment without trapping the
// outer terminal in a half-decoded escape sequence.
func TestRender_ControlCharsBecomeSpaces(t *testing.T) {
	cells := [][]pty.Cell{
		{
			{Char: 'a', FG: 7, BG: 0},
			{Char: 0x07, FG: 7, BG: 0}, // BEL
			{Char: 0x1b, FG: 7, BG: 0}, // ESC
			{Char: 'b', FG: 7, BG: 0},
		},
	}
	s := &pty.Screen{Cols: 4, Rows: 1, Cells: cells}
	out := stripANSI(Render(s, nil))
	if out != "a  b" {
		t.Errorf("got %q, want %q", out, "a  b")
	}
}

// TestRender_StyleEscapesEmitted: bold/italic/underline attributes
// produce ANSI SGR sequences in the output. The exact byte count
// varies across lipgloss versions, so assert via direct presence
// of the SGR codes (1=bold, 3=italic, 4=underline) inside CSI
// `\x1b[ … m` introducers.
func TestRender_StyleEscapesEmitted(t *testing.T) {
	cases := []struct {
		name      string
		attrs     uint16
		wantSGR   string // single SGR digit we expect to appear in a CSI
	}{
		{"bold", pty.AttrBold, "1"},
		{"italic", pty.AttrItalic, "3"},
		{"underline", pty.AttrUnderline, "4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cells := [][]pty.Cell{
				{
					{Char: 'a', FG: 1, BG: 0, Attrs: c.attrs},
					{Char: 'b', FG: 1, BG: 0, Attrs: c.attrs},
				},
			}
			s := &pty.Screen{Cols: 2, Rows: 1, Cells: cells}
			out := Render(s, nil)
			// Look for "\x1b[" followed eventually by the SGR digit
			// then "m". A simple substring check works because
			// lipgloss separates SGRs with `;`.
			if !containsSGR(out, c.wantSGR) {
				t.Errorf("%s SGR (\\x1b[…%sm) not found in output: %q",
					c.name, c.wantSGR, out)
			}
		})
	}
}

// containsSGR reports whether the output contains a CSI sequence
// (ESC `[` … `m`) whose semicolon-separated parameters include the
// given SGR digit. Catches `\x1b[1m`, `\x1b[31;1m`, `\x1b[1;38;2;…m`.
func containsSGR(s, sgr string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		// Find terminator 'm' (or other final byte).
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7E) {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			continue
		}
		params := s[i+2 : j]
		// Walk semicolon-separated params; match exact == sgr.
		for _, p := range strings.Split(params, ";") {
			if p == sgr {
				return true
			}
		}
	}
	return false
}

// TestRender_NilScreenSafe: nil / zero-row / zero-col return empty
// rather than panicking. The TUI calls Render on every refresh; a
// brief race where the screen isn't yet populated must not crash.
func TestRender_NilScreenSafe(t *testing.T) {
	if got := Render(nil, nil); got != "" {
		t.Errorf("Render(nil) = %q, want empty", got)
	}
	if got := Render(&pty.Screen{Rows: 0, Cols: 0}, nil); got != "" {
		t.Errorf("Render(empty) = %q, want empty", got)
	}
}

// TestRender_MaxRowsClamps: MaxRows truncates the output to the first
// N rows. Useful when a tool-result block is shorter than the spawned
// PTY.
func TestRender_MaxRowsClamps(t *testing.T) {
	s := fixture("row1", "row2", "row3", "row4")
	opts := &RenderOpts{MaxRows: 2}
	out := stripANSI(Render(s, opts))
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out)
	}
	if lines[0] != "row1" || lines[1] != "row2" {
		t.Errorf("got %v, want [row1 row2]", lines)
	}
}

// TestRender_CursorHighlight: when CursorBg is set and the cursor is
// visible, the cell at (Cursor.X, Cursor.Y) gets a different background
// than its neighbours. Asserts via byte-length: a styled cursor cell
// produces a longer output than the same screen without the cursor.
func TestRender_CursorHighlight(t *testing.T) {
	cells := [][]pty.Cell{
		{
			{Char: 'a', FG: 7, BG: 0},
			{Char: 'b', FG: 7, BG: 0},
			{Char: 'c', FG: 7, BG: 0},
		},
	}
	s := &pty.Screen{Cols: 3, Rows: 1, Cells: cells, Cursor: pty.CursorPos{X: 1, Y: 0, Visible: true}}

	withoutCursor := Render(s, &RenderOpts{}) // CursorBg empty
	withCursor := Render(s, &RenderOpts{CursorBg: "#9aa4b1"})

	if withCursor == withoutCursor {
		t.Errorf("cursor highlight should change output; both = %q", withCursor)
	}
	if len(withCursor) <= len(withoutCursor) {
		t.Errorf("with-cursor output (%d) should be longer than without (%d)", len(withCursor), len(withoutCursor))
	}
}

// stripANSI removes ANSI CSI escape sequences from a string. Used in
// tests where we care about the visible characters only, not the
// styling bytes lipgloss sprinkles in.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until terminator (a final byte in 0x40..0x7E).
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7E) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
