package tui

import (
	"fmt"
	"strings"

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

// runeLen returns the number of runes (≈ display columns for our
// ASCII / box-char alphabet) in s. Use this anywhere a width
// comparison is being made — len() gives bytes and silently
// underflows pad calculations on multi-byte content.
func runeLen(s string) int { return len([]rune(s)) }

// truncRunes truncates s to at most n runes. n ≤ 0 returns "".
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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
	var out []string
	var cur strings.Builder
	for _, word := range strings.Fields(line) {
		// Single oversized word.
		if len(word) > width {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			for len(word) > width {
				out = append(out, word[:width])
				word = word[width:]
			}
			cur.WriteString(word)
			continue
		}
		if cur.Len() == 0 {
			cur.WriteString(word)
			continue
		}
		if cur.Len()+1+len(word) > width {
			out = append(out, cur.String())
			cur.Reset()
			cur.WriteString(word)
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(word)
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
		if len(p.Label) > labelW {
			labelW = len(p.Label)
		}
	}
	indent := "    "                                  // body indent
	valIndent := indent + strings.Repeat(" ", labelW+2) // continuation rows align under value
	for _, p := range pairs {
		labelPad := strings.Repeat(" ", labelW-len(p.Label))
		first := indent + p.Label + ":" + labelPad + " "
		// First line gets the label; wrap value into the value column.
		valWidth := w - len(first)
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
		valWidth := w - len(indent) - len(prefix)
		if valWidth < 1 {
			valWidth = 1
		}
		lines := wrapWords(item, valWidth)
		writeRow(b, w, indent+prefix+lines[0])
		contIndent := indent + strings.Repeat(" ", len(prefix))
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
	for i, c := range table.Columns {
		widths[i] = len(c)
	}
	for _, row := range table.Rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
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
