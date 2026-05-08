// SVG and text rendering for Screen snapshots.
//
// The SVG output is a self-contained <svg> document — no external
// fonts, no <link>s — sized to fit the cell grid at a fixed monospace
// metric. Two passes: first a single combined background path per
// background color (one <path> per distinct BG, runs of cells merged
// into rect "M x y H xx V yy Z" subpaths to keep the file small),
// then one <text> per row with <tspan>s grouping consecutive cells
// that share fg+attrs. Cursor (when visible) is drawn last as an
// outlined rect so it sits above text without occluding the glyph.
//
// The two-pass split matters for size: a 120×32 grid is 3840 cells;
// a naive cell-at-a-time SVG runs ~600 KB. Backgrounds-as-paths +
// merged-tspan-text drops a typical screen to 30–60 KB.
package pty

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Defaults picked to match a typical TUI font (JetBrains Mono 13px)
// and the bridge's dark theme. Operators driving Snapshot directly
// pass overrides via SVGOpts; the bundled tool exposes them as
// optional args.
const (
	defaultCellW   = 8.0  // px advance per column
	defaultCellH   = 17.0 // px line height
	defaultBaseY   = 13.0 // y of text baseline within a row
	defaultFontPx  = 13
	defaultBgColor = 0x0E1117 // canvas background (matches bridge index.html)
	defaultFgColor = 0xE8EAED // default text color (matches bridge)
	defaultCursor  = 0x9AA4B1 // matches bridge cursor color
)

// SVGOpts overrides cell metrics and palette choices. Zero-valued
// fields fall back to the constants above. Most callers should leave
// it nil — the defaults match the bundled font assumption.
type SVGOpts struct {
	CellW    float64 // pixel width per column
	CellH    float64 // pixel height per row
	FontPx   int     // CSS font-size for <text>
	BgColor  uint32  // 0xRRGGBB canvas background
	FgColor  uint32  // 0xRRGGBB default text color
	CursorBG uint32  // 0xRRGGBB cursor outline
}

func (o *SVGOpts) cellW() float64 {
	if o != nil && o.CellW > 0 {
		return o.CellW
	}
	return defaultCellW
}
func (o *SVGOpts) cellH() float64 {
	if o != nil && o.CellH > 0 {
		return o.CellH
	}
	return defaultCellH
}
func (o *SVGOpts) fontPx() int {
	if o != nil && o.FontPx > 0 {
		return o.FontPx
	}
	return defaultFontPx
}
func (o *SVGOpts) bg() uint32 {
	if o != nil && o.BgColor != 0 {
		return o.BgColor
	}
	return defaultBgColor
}
func (o *SVGOpts) fg() uint32 {
	if o != nil && o.FgColor != 0 {
		return o.FgColor
	}
	return defaultFgColor
}
func (o *SVGOpts) cursor() uint32 {
	if o != nil && o.CursorBG != 0 {
		return o.CursorBG
	}
	return defaultCursor
}

// Text returns the screen as a row-per-line string with trailing
// whitespace trimmed and empty trailing rows dropped. Empty cells
// (Char==0) become spaces so column alignment is preserved within a
// line; trailing spaces on each row are still trimmed.
func (s *Screen) Text() string {
	if s == nil || s.Rows == 0 {
		return ""
	}
	rows := make([]string, 0, s.Rows)
	for y := 0; y < s.Rows; y++ {
		var b strings.Builder
		b.Grow(s.Cols)
		for x := 0; x < s.Cols; x++ {
			ch := s.Cells[y][x].Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n")
}

// SVG renders the screen as a self-contained SVG document. opts may
// be nil to accept the defaults.
func (s *Screen) SVG(opts *SVGOpts) string {
	if s == nil || s.Rows == 0 {
		return ""
	}
	cellW := opts.cellW()
	cellH := opts.cellH()
	width := cellW * float64(s.Cols)
	height := cellH * float64(s.Rows)
	bg := opts.bg()
	fg := opts.fg()
	cur := opts.cursor()

	var b strings.Builder
	b.Grow(8 * 1024) // typical 120×32 lands around 30–60 KB; this is preallocation, not a cap.
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" font-family="ui-monospace,'JetBrains Mono','Fira Code',monospace" font-size="%d" shape-rendering="crispEdges">`,
		width, height, width, height, opts.fontPx())
	fmt.Fprintf(&b, `<rect width="%.0f" height="%.0f" fill="#%06x"/>`, width, height, bg)

	writeBackgrounds(&b, s, cellW, cellH)
	writeText(&b, s, cellW, cellH, fg)
	if s.Cursor.Visible && s.Cursor.Y >= 0 && s.Cursor.Y < s.Rows && s.Cursor.X >= 0 && s.Cursor.X < s.Cols {
		fmt.Fprintf(&b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="none" stroke="#%06x" stroke-width="1"/>`,
			float64(s.Cursor.X)*cellW, float64(s.Cursor.Y)*cellH, cellW, cellH, cur)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// writeBackgrounds emits one <rect> per run of consecutive same-BG
// cells. For terminal output that's mostly the default background,
// this collapses to a few dozen rects per screen — much smaller than
// one rect per cell. Cells with the default BG are skipped (the
// canvas <rect> covers them).
//
// AttrReverse swaps fg/bg at render time, so a reversed cell with
// "default" BG still gets an explicit background rect emitted from
// its FG color — handled here by computing the effective BG up front.
func writeBackgrounds(b *strings.Builder, s *Screen, cellW, cellH float64) {
	for y := 0; y < s.Rows; y++ {
		x := 0
		for x < s.Cols {
			cell := s.Cells[y][x]
			ebg, _ := effectiveColors(cell)
			if cell.IsDefaultBG() && cell.Attrs&AttrReverse == 0 {
				x++
				continue
			}
			run := 1
			for x+run < s.Cols {
				next := s.Cells[y][x+run]
				nbg, _ := effectiveColors(next)
				if nbg != ebg || (next.IsDefaultBG() && next.Attrs&AttrReverse == 0) {
					break
				}
				run++
			}
			fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="#%06x"/>`,
				float64(x)*cellW, float64(y)*cellH, cellW*float64(run), cellH, ebg)
			x += run
		}
	}
}

// writeText emits one <text> element per row, with consecutive cells
// that share fg+attrs grouped into a single <tspan>. The tspan's x
// attribute pins each glyph to its column; we don't rely on tspan
// natural advance because the renderer's font metrics may not match
// our cellW exactly.
func writeText(b *strings.Builder, s *Screen, cellW, cellH float64, defaultFG uint32) {
	for y := 0; y < s.Rows; y++ {
		// Skip rows that contain nothing but spaces: an empty <text>
		// element is wasted bytes and the renderer would have nothing
		// to draw anyway.
		if rowIsBlank(s.Cells[y]) {
			continue
		}
		baseY := float64(y)*cellH + defaultBaseY
		fmt.Fprintf(b, `<text y="%.0f">`, baseY)
		x := 0
		for x < s.Cols {
			cell := s.Cells[y][x]
			ch := cell.Char
			if ch == 0 || ch == ' ' {
				x++
				continue
			}
			_, efg := effectiveColors(cell)
			attrs := cell.Attrs
			run := []rune{ch}
			runStartX := x
			x++
			for x < s.Cols {
				next := s.Cells[y][x]
				if next.Char == 0 || next.Char == ' ' {
					break
				}
				_, nfg := effectiveColors(next)
				if nfg != efg || next.Attrs != attrs {
					break
				}
				run = append(run, next.Char)
				x++
			}
			emitTspan(b, runStartX, cellW, run, efg, attrs, defaultFG)
		}
		b.WriteString(`</text>`)
	}
}

func emitTspan(b *strings.Builder, col int, cellW float64, run []rune, fg uint32, attrs uint16, defaultFG uint32) {
	xPx := float64(col) * cellW
	fmt.Fprintf(b, `<tspan x="%.0f"`, xPx)
	if fg != defaultFG {
		fmt.Fprintf(b, ` fill="#%06x"`, fg)
	}
	if attrs&AttrBold != 0 {
		b.WriteString(` font-weight="bold"`)
	}
	if attrs&AttrItalic != 0 {
		b.WriteString(` font-style="italic"`)
	}
	if attrs&AttrUnderline != 0 {
		b.WriteString(` text-decoration="underline"`)
	}
	b.WriteString(`>`)
	// XML-escape the content. We need to escape <, >, &, and the two
	// quote forms — but not the more exotic XML-1.0 control chars,
	// since the screen contains glyphs that have already been resolved
	// from a UTF-8 stream by vt10x.
	for _, r := range run {
		switch r {
		case '&':
			b.WriteString(`&amp;`)
		case '<':
			b.WriteString(`&lt;`)
		case '>':
			b.WriteString(`&gt;`)
		case '"':
			b.WriteString(`&quot;`)
		case '\'':
			b.WriteString(`&apos;`)
		default:
			if r < 0x20 || !utf8.ValidRune(r) {
				b.WriteRune(' ')
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteString(`</tspan>`)
}

func rowIsBlank(row []Cell) bool {
	for i := range row {
		ch := row[i].Char
		if ch != 0 && ch != ' ' {
			return false
		}
	}
	return true
}

// effectiveColors applies AttrReverse and the ANSI palette to produce
// the final RGB pair the SVG renderer should use. When reverse is set,
// fg and bg swap (matching xterm behaviour). When a color is the
// "default" sentinel (FG=7, BG=0 in vt10x) we substitute the renderer's
// configured defaults so the cell paints on the canvas color.
func effectiveColors(c Cell) (bg, fg uint32) {
	bg = ColorRGB(c.BG)
	fg = ColorRGB(c.FG)
	if c.IsDefaultBG() {
		bg = defaultBgColor
	}
	if c.IsDefaultFG() {
		fg = defaultFgColor
	}
	if c.Attrs&AttrReverse != 0 {
		bg, fg = fg, bg
	}
	return bg, fg
}
