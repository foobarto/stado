// Package ptyblock renders a pty.Screen (the cell grid produced by
// shell.snapshot's vt10x emulator) as styled terminal text suitable
// for embedding inside the TUI's tool-output area.
//
// The output is a sequence of strings — one per visible row — each
// already wrapped in lipgloss escape codes for fg / bg / bold /
// italic / underline / inverse. Callers concatenate them with "\n"
// and stamp them into a viewport.
//
// The renderer is the building block for the interactive-shell
// feature (TAB-to-enter / SHIFT-TAB-to-leave PTY overlay): given a
// snapshot taken from the daemon's pty.Manager, produce a faithful
// terminal-style view the operator can read and (eventually) drive.
//
// Pure function — no bubbletea dependency, no I/O. Easy to unit test
// against fixture screens.
package ptyblock

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
)

// RenderOpts overrides the default style choices. Zero-valued fields
// fall back to defaults: dark theme matching the bridge's xterm.js
// palette, no cursor marker (caller can layer one on top), no border.
type RenderOpts struct {
	// DefaultBg is the canvas background color rendered for cells that
	// don't have an explicit BG (or whose BG resolves to the
	// "default-bg" sentinel — vt10x's BG=0). Lipgloss accepts this as a
	// "#RRGGBB" hex string.
	DefaultBg string

	// DefaultFg is the text color for cells without an explicit FG
	// (vt10x's FG=7). Same encoding as DefaultBg.
	DefaultFg string

	// CursorBg is the background color drawn behind the cursor cell.
	// Empty = no cursor highlight (the renderer just emits the glyph
	// in normal style). Useful when the embedding TUI provides its
	// own focus indicator.
	CursorBg string

	// MaxRows / MaxCols clamp the rendered output. 0 = full screen.
	// Useful for previews or when the tool-result block is narrower
	// than the spawned PTY.
	MaxRows int
	MaxCols int
}

// Default style values. These match the bridge's xterm.js theme and
// the SVG renderer's defaults (internal/plugins/runtime/pty/svg.go),
// so a snapshot rendered as text in the TUI looks like the same
// snapshot rendered as SVG outside it. cursorBg is exported as a
// constant for callers building RenderOpts (the embedding TUI's
// PTY block component picks it up when the operator hits TAB to
// enter the shell).
const (
	defaultBg = "#0E1117"
	defaultFg = "#E8EAED"
	// CursorBgDefault is the bridge's xterm.js cursor color. Exported
	// so callers can spell `RenderOpts{CursorBg: ptyblock.CursorBgDefault}`
	// without re-typing the hex literal.
	CursorBgDefault = "#9AA4B1"
)

// Render produces a multi-line string suitable for printing in a
// terminal — one row of the screen per line, lipgloss-styled. Empty
// trailing rows are dropped; trailing whitespace per row is preserved
// (otherwise wide-cell highlight runs would break at the edge).
func Render(s *pty.Screen, opts *RenderOpts) string {
	if s == nil || s.Rows == 0 || s.Cols == 0 {
		return ""
	}
	bg := chooseStr(opts != nil, getStr(opts, "DefaultBg"), defaultBg)
	fg := chooseStr(opts != nil, getStr(opts, "DefaultFg"), defaultFg)
	cursorBg := getStr(opts, "CursorBg")

	rows := s.Rows
	cols := s.Cols
	if opts != nil && opts.MaxRows > 0 && opts.MaxRows < rows {
		rows = opts.MaxRows
	}
	if opts != nil && opts.MaxCols > 0 && opts.MaxCols < cols {
		cols = opts.MaxCols
	}

	var b strings.Builder
	b.Grow(rows * cols * 8) // rough preallocation for styled cells.

	for y := 0; y < rows; y++ {
		row := s.Cells[y]
		// Group consecutive cells that share fg+bg+attrs into a single
		// lipgloss-styled run. Without grouping, every cell would emit
		// its own ANSI prologue/epilogue and the output would balloon
		// past what's necessary.
		x := 0
		for x < cols {
			start := x
			cell := row[x]
			cellAt := func(i int) pty.Cell { return row[i] }
			x++
			for x < cols && sameStyle(cellAt(x), cell) {
				x++
			}
			b.WriteString(renderRun(row[start:x], cell, fg, bg, cursorBg, s.Cursor, y, start))
		}
		if y < rows-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// sameStyle returns true when two cells share fg, bg, and attribute
// bits — runs with the same style merge into one lipgloss render
// for size + speed.
func sameStyle(a, b pty.Cell) bool {
	return a.FG == b.FG && a.BG == b.BG && a.Attrs == b.Attrs
}

// renderRun applies the run's shared style to its glyphs. The cursor
// cell, when it lands inside the run and CursorBg is set, gets its
// own one-cell highlight wedge inside the run's style.
func renderRun(run []pty.Cell, sample pty.Cell, defaultFg, defaultBg, cursorBg string, cursor pty.CursorPos, row, startCol int) string {
	style := buildStyle(sample, defaultFg, defaultBg)

	// Fast path: cursor not in this run, or cursor highlighting off.
	if cursorBg == "" || !cursor.Visible || cursor.Y != row || cursor.X < startCol || cursor.X >= startCol+len(run) {
		return style.Render(runText(run))
	}

	// Cursor lands inside the run: split into prefix + cursor cell +
	// suffix, render each with the appropriate style.
	cursorIdx := cursor.X - startCol
	var b strings.Builder
	if cursorIdx > 0 {
		b.WriteString(style.Render(runText(run[:cursorIdx])))
	}
	cursorStyle := style.Background(lipgloss.Color(cursorBg))
	b.WriteString(cursorStyle.Render(runText(run[cursorIdx : cursorIdx+1])))
	if cursorIdx+1 < len(run) {
		b.WriteString(style.Render(runText(run[cursorIdx+1:])))
	}
	return b.String()
}

// buildStyle resolves a cell's color/attr bits into a lipgloss.Style.
// AttrReverse swaps fg/bg as the convention the snapshot SVG also
// follows. Default colors (FG=7, BG=0 in vt10x) substitute the
// renderer's configured defaults so unwritten cells paint on the
// canvas color.
func buildStyle(c pty.Cell, defaultFg, defaultBg string) lipgloss.Style {
	style := lipgloss.NewStyle()
	fg := defaultFg
	bg := defaultBg
	if !c.IsDefaultFG() {
		fg = fmt.Sprintf("#%06x", pty.ColorRGB(c.FG))
	}
	if !c.IsDefaultBG() {
		bg = fmt.Sprintf("#%06x", pty.ColorRGB(c.BG))
	}
	if c.Attrs&pty.AttrReverse != 0 {
		fg, bg = bg, fg
	}
	style = style.Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
	if c.Attrs&pty.AttrBold != 0 {
		style = style.Bold(true)
	}
	if c.Attrs&pty.AttrItalic != 0 {
		style = style.Italic(true)
	}
	if c.Attrs&pty.AttrUnderline != 0 {
		style = style.Underline(true)
	}
	return style
}

// runText extracts the printable glyphs from a run. Empty cells
// (Char == 0) become spaces — preserves column alignment for runs
// that include whitespace gaps. Control characters (< 0x20) and
// invalid runes are also rendered as spaces; emitting them literally
// would trip the embedding terminal's own escape handling and break
// styling.
func runText(run []pty.Cell) string {
	var b strings.Builder
	b.Grow(len(run))
	for _, c := range run {
		r := c.Char
		if r == 0 {
			b.WriteRune(' ')
			continue
		}
		if r < 0x20 || !utf8.ValidRune(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func getStr(opts *RenderOpts, field string) string {
	if opts == nil {
		return ""
	}
	switch field {
	case "DefaultBg":
		return opts.DefaultBg
	case "DefaultFg":
		return opts.DefaultFg
	case "CursorBg":
		return opts.CursorBg
	}
	return ""
}

func chooseStr(haveOpts bool, supplied, fallback string) string {
	if haveOpts && supplied != "" {
		return supplied
	}
	return fallback
}
