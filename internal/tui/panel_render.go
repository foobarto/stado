package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// panelRenderWidth is the inner content width used by renderPanelASCII.
// Matches the most-common terminal width (80) minus 2 for the left/right
// border chars. Long lines wrap inside the inner area; short lines are
// padded out to the full width so the right border lines up.
//
// A future F9b enhancement could thread the live terminal width here, but
// renderPanelASCII is pure (no model dependency) and a fixed 78-char inner
// width matches the aesthetic of the existing rounded-border overlays
// (quit_confirm.go / overlays/help.go) which also size for ~80-col tty.
const panelRenderWidth = 78

// boxTopLeft / boxTopRight / boxBottomLeft / boxBottomRight / boxHorizontal
// / boxVertical mirror lipgloss.RoundedBorder so the panel sits visually
// next to the existing rounded overlays without importing lipgloss into the
// pure-string renderer.
const (
	boxTopLeft     = "╭"
	boxTopRight    = "╮"
	boxBottomLeft  = "╰"
	boxBottomRight = "╯"
	boxHorizontal  = "─"
	boxVertical    = "│"
	sectionDivider = "─"
)

// RenderPanelASCIIPublic exposes renderPanelASCII for callers outside
// this package (currently the MCP server: it renders panels as the
// "functionally equivalent unstructured content" the MCP spec asks
// to accompany StructuredContent). The internal renderPanelASCII
// stays private as the canonical entry point — public wrapper kept
// thin so adding theme/width parameters later doesn't churn the
// public surface unnecessarily. F9b.4.
func RenderPanelASCIIPublic(panel pluginRuntime.Panel) string {
	return renderPanelASCII(panel)
}

// renderPanelASCII renders a Panel into a multi-line bordered string.
//
// Layout shape:
//
//	╭─ Title (variant) ────────────────────────────────────────────────────╮
//	│  Heading                                                             │
//	│    body line 1                                                       │
//	│    body line 2                                                       │
//	│  ────────────────────────────────────────────────────────────────────│
//	│  Heading 2                                                           │
//	│    key1: value1                                                      │
//	│    key2: value2                                                      │
//	│                                                                      │
//	│  footer text                                                         │
//	╰──────────────────────────────────────────────────────────────────────╯
//
// Inner width is panelRenderWidth chars; lines that exceed the width
// wrap on word boundaries. Tables and code blocks that exceed width
// are NOT wrapped (preserving column alignment / verbatim formatting)
// and may visually overflow narrow terminals — same trade-off the
// existing tool-output renderer makes for verbatim payloads.
//
// Spec: F9b.2 (.agent/specs/open/f9b-ui-render.md).
func renderPanelASCII(panel pluginRuntime.Panel) string {
	var b strings.Builder
	w := panelRenderWidth

	// Title row.
	titleText := panel.Title
	if panel.Variant != "" {
		titleText = fmt.Sprintf("%s (%s)", panel.Title, panel.Variant)
	}
	writeTopBorder(&b, w, " "+titleText+" ")
	b.WriteByte('\n')

	// Sections.
	for i, sec := range panel.Sections {
		if i > 0 {
			writeDividerRow(&b, w)
			b.WriteByte('\n')
		}
		writeSection(&b, w, sec)
	}

	// Footer (optional).
	if panel.Footer != "" {
		writeDividerRow(&b, w)
		b.WriteByte('\n')
		writeWrappedRows(&b, w, panel.Footer, "  ")
	}

	writeBottomBorder(&b, w)
	return b.String()
}

// writeTopBorder emits the top border with a left-justified label that
// is hyphenated into the horizontal rule. If the label is too long,
// it is truncated to fit (keeps the border aligned). Width arithmetic
// here and below is rune-based — body content may contain multi-byte
// runes (the "›" truncation marker, box chars used inside table cells)
// and byte-based padding would silently misalign the right border.
func writeTopBorder(b *strings.Builder, w int, label string) {
	b.WriteString(boxTopLeft)
	b.WriteString(boxHorizontal)
	maxLabel := w - 2
	label = truncRunes(label, maxLabel)
	b.WriteString(label)
	used := 1 + runeLen(label) // leading ─ already counted by w-2
	for i := used; i < w; i++ {
		b.WriteString(boxHorizontal)
	}
	b.WriteString(boxTopRight)
}

func writeBottomBorder(b *strings.Builder, w int) {
	b.WriteString(boxBottomLeft)
	for i := 0; i < w; i++ {
		b.WriteString(boxHorizontal)
	}
	b.WriteString(boxBottomRight)
}

// writeDividerRow emits a within-panel separator row spanning the full
// inner width. Distinct from the top/bottom borders — sits between
// adjacent sections to visually delimit them.
func writeDividerRow(b *strings.Builder, w int) {
	b.WriteString(boxVertical)
	for i := 0; i < w; i++ {
		b.WriteString(sectionDivider)
	}
	b.WriteString(boxVertical)
}

// writeRow emits one inner content row: │<content padded/truncated>│.
// Padding/truncation in rune-space so multi-byte content (truncation
// marker, embedded box chars) doesn't break right-border alignment.
func writeRow(b *strings.Builder, w int, content string) {
	b.WriteString(boxVertical)
	content = truncRunes(content, w)
	b.WriteString(content)
	for i := runeLen(content); i < w; i++ {
		b.WriteByte(' ')
	}
	b.WriteString(boxVertical)
	b.WriteByte('\n')
}

// runeLen returns the DISPLAY-COLUMN width of s. Body content comes
// from plugin output and may carry wide-CJK / emoji graphemes whose
// rune count is ~half their column width — counting runes (or bytes)
// silently under-pads and shoves the right border out of alignment.
// ansi.StringWidth is display-width- and grapheme-aware.
func runeLen(s string) int { return ansi.StringWidth(s) }

// truncRunes truncates s to at most n DISPLAY COLUMNS. n ≤ 0 returns
// "". Wide-CJK / emoji safe: a rune-count slice over-budgeted a row to
// ~2x its column width and could split a wide rune mid-grapheme,
// leaking invalid UTF-8. ansi.Truncate never splits a grapheme.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(s, n, "")
}

// writeWrappedRows splits text on newlines, then word-wraps each line
// to fit (w - len(indent)) chars and emits one row per wrapped line
// with the given indent. Used by text bodies, footer, headings.
func writeWrappedRows(b *strings.Builder, w int, text, indent string) {
	contentWidth := w - len(indent)
	if contentWidth < 1 {
		contentWidth = 1
	}
	for _, line := range strings.Split(text, "\n") {
		for _, wrapped := range wrapWords(line, contentWidth) {
			writeRow(b, w, indent+wrapped)
		}
	}
}

// wrapWords does word-wrap for a single physical line. Long single
// words longer than width are forcibly broken (uncommon — only paths,
// URLs, base64). Returns at least one entry (the empty string for
// empty input — preserves blank lines in text bodies).
func wrapWords(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if width < 1 {
		width = 1
	}
	var out []string
	var cur strings.Builder
	curW := 0 // display width of cur
	for _, word := range strings.Fields(line) {
		// Single oversized word. Break it on DISPLAY-COLUMN boundaries
		// via ansi.Cut so a wide-CJK / emoji grapheme is never split
		// mid-rune (a byte slice word[:width] leaked invalid UTF-8 and
		// over-budgeted the row to ~2x its column width).
		if ansi.StringWidth(word) > width {
			if curW > 0 {
				out = append(out, cur.String())
				cur.Reset()
				curW = 0
			}
			for ansi.StringWidth(word) > width {
				head := ansi.Cut(word, 0, width)
				if head == "" {
					// A single grapheme wider than the budget (width <
					// rune column width). Emit one grapheme so the loop
					// always makes progress instead of spinning forever.
					_, size := utf8.DecodeRuneInString(word)
					head = word[:size]
				}
				out = append(out, head)
				word = word[len(head):]
			}
			cur.WriteString(word)
			curW = ansi.StringWidth(word)
			continue
		}
		ww := ansi.StringWidth(word)
		if curW == 0 {
			cur.WriteString(word)
			curW = ww
			continue
		}
		if curW+1+ww > width {
			out = append(out, cur.String())
			cur.Reset()
			cur.WriteString(word)
			curW = ww
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(word)
		curW += 1 + ww
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// writeSection dispatches body rendering by section kind. Heading,
// when present, gets a row of its own before the body. F9b.2.
func writeSection(b *strings.Builder, w int, sec pluginRuntime.Section) {
	if sec.Heading != "" {
		writeRow(b, w, "  "+sec.Heading)
	}
	switch sec.Kind {
	case "text":
		writeWrappedRows(b, w, sec.Text, "    ")
	case "kv":
		writeKV(b, w, sec.KV)
	case "list":
		writeList(b, w, sec.List)
	case "code":
		writeCode(b, w, sec.Code)
	case "table":
		writeTable(b, w, sec.Table)
	case "diff":
		writeDiff(b, w, sec.Diff)
	}
}

// writeKV renders a kv body as aligned label/value columns. Label
// column is right-padded to the longest label width so values line
// up. Long values wrap onto continuation rows indented to value
// column. F9b.2.
func writeKV(b *strings.Builder, w int, pairs []pluginRuntime.KVPair) {
	labelW := 0
	for _, p := range pairs {
		if lw := runeLen(p.Label); lw > labelW {
			labelW = lw
		}
	}
	indent := "    "                                    // body indent
	valIndent := indent + strings.Repeat(" ", labelW+2) // continuation rows align under value
	for _, p := range pairs {
		labelPad := strings.Repeat(" ", labelW-runeLen(p.Label))
		first := indent + p.Label + ":" + labelPad + " "
		// First line gets the label; wrap value into the value column.
		valWidth := w - runeLen(first)
		if valWidth < 1 {
			valWidth = 1
		}
		lines := wrapWords(p.Value, valWidth)
		writeRow(b, w, first+lines[0])
		for _, cont := range lines[1:] {
			writeRow(b, w, valIndent+cont)
		}
	}
}

// writeList renders a list body with marker per Marker kind.
// F9b.2.
func writeList(b *strings.Builder, w int, list pluginRuntime.ListBody) {
	indent := "    "
	for i, item := range list.Items {
		var prefix string
		switch list.Marker {
		case "numbered":
			prefix = fmt.Sprintf("%d. ", i+1)
		case "check":
			prefix = "[ ] "
		default: // "bullet" or ""
			prefix = "• "
		}
		// First line gets prefix; continuations align under text.
		valWidth := w - runeLen(indent) - runeLen(prefix)
		if valWidth < 1 {
			valWidth = 1
		}
		lines := wrapWords(item, valWidth)
		writeRow(b, w, indent+prefix+lines[0])
		contIndent := indent + strings.Repeat(" ", runeLen(prefix))
		for _, cont := range lines[1:] {
			writeRow(b, w, contIndent+cont)
		}
	}
}

// writeCode renders a code body verbatim with a 4-space indent. The
// language hint, when present, is prepended on its own line. Lines
// longer than the inner width are truncated rather than wrapped so
// the code stays visually verbatim. F9b.2.
func writeCode(b *strings.Builder, w int, code pluginRuntime.CodeBody) {
	indent := "    "
	if code.Language != "" {
		writeRow(b, w, indent+"["+code.Language+"]")
	}
	contentWidth := w - runeLen(indent)
	for _, line := range strings.Split(code.Content, "\n") {
		if runeLen(line) > contentWidth {
			line = truncRunes(line, contentWidth-1) + "›"
		}
		writeRow(b, w, indent+line)
	}
}

// writeTable renders a table body as an ASCII grid with column-width
// detection. Long cells are truncated (with a "›" marker) to keep
// columns aligned; if the total computed width exceeds the inner
// panel width, columns are proportionally narrowed. F9b.2.
func writeTable(b *strings.Builder, w int, table pluginRuntime.TableBody) {
	indent := "    "
	cols := len(table.Columns)
	widths := make([]int, cols)
	// Column widths are measured in DISPLAY COLUMNS (not bytes): cells
	// come from plugin output and may carry wide-CJK / emoji. byte len
	// over-allocated wide cells to ~3x and writeTableRow then pads/truncs
	// in display space, so the row overflowed the panel border.
	for i, c := range table.Columns {
		widths[i] = runeLen(c)
	}
	for _, row := range table.Rows {
		for i, cell := range row {
			if i < cols && runeLen(cell) > widths[i] {
				widths[i] = runeLen(cell)
			}
		}
	}
	// Total = sum(widths) + 3*(cols-1) [" │ " separators] + 4 (indent).
	total := len(indent)
	for _, ww := range widths {
		total += ww
	}
	total += 3 * (cols - 1)
	// Narrow columns proportionally if oversized. Each column gets at
	// least 1 char.
	if total > w {
		over := total - w
		// Distribute the deficit over columns proportional to widths.
		for i := range widths {
			if widths[i] <= 1 {
				continue
			}
			cut := widths[i] * over / total
			if cut < 1 {
				cut = 1
			}
			widths[i] -= cut
			if widths[i] < 1 {
				widths[i] = 1
			}
		}
	}

	writeTableRow(b, w, indent, widths, table.Columns)
	// Column header underline: dashes per column.
	underline := make([]string, cols)
	for i, ww := range widths {
		underline[i] = strings.Repeat("─", ww)
	}
	writeTableRow(b, w, indent, widths, underline)
	for _, row := range table.Rows {
		writeTableRow(b, w, indent, widths, row)
	}
}

// writeTableRow emits one table row with cells truncated/padded to
// each column's width and " │ " separators between cells. Width
// arithmetic in rune-space so the truncation marker (multi-byte "›")
// doesn't desynchronise the padding.
func writeTableRow(b *strings.Builder, w int, indent string, widths []int, cells []string) {
	parts := make([]string, len(cells))
	for i, cell := range cells {
		if runeLen(cell) > widths[i] {
			// Reserve one rune for the truncation marker; if widths[i]
			// is 1 we have no room for any actual content, so just emit
			// the marker.
			if widths[i] <= 1 {
				cell = "›"
			} else {
				cell = truncRunes(cell, widths[i]-1) + "›"
			}
		}
		pad := widths[i] - runeLen(cell)
		if pad < 0 {
			pad = 0
		}
		parts[i] = cell + strings.Repeat(" ", pad)
	}
	writeRow(b, w, indent+strings.Join(parts, " │ "))
}

// writeDiff renders a diff body as before / after side-by-side line
// markers: "-" for Before lines, "+" for After lines. The simplest
// possible visualisation; richer alignment (Myers diff) is a future
// enhancement that would require pulling in go-difflib. For F9b.2
// we keep the dependency surface zero.
func writeDiff(b *strings.Builder, w int, diff pluginRuntime.DiffBody) {
	indent := "    "
	contentWidth := w - len(indent) - 2 // 2 for "- " / "+ "
	for _, line := range strings.Split(diff.Before, "\n") {
		for _, wrapped := range wrapWords(line, contentWidth) {
			writeRow(b, w, indent+"- "+wrapped)
		}
	}
	for _, line := range strings.Split(diff.After, "\n") {
		for _, wrapped := range wrapWords(line, contentWidth) {
			writeRow(b, w, indent+"+ "+wrapped)
		}
	}
}
