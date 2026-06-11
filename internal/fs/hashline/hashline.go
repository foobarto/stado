// Package hashline implements content-anchored ("hashline") file editing —
// the read-prefix + edit-anchor contract shared, byte-identically, by the
// native fs tools (internal/fs) and the bundled wasm fs plugin
// (plugins/bundled/fs). It is the SINGLE SOURCE OF TRUTH for:
//
//   - the LINE#HASH: read-output prefix every line carries, and
//   - the {op,pos,end,lines} anchor edit schema with hash validation,
//     stale-anchor rejection, and the literal-content guard.
//
// Inspired by https://github.com/RimuruW/pi-hashline-edit (itself adapted
// from oh-my-pi). stado swaps the protocol's xxHash32 for FNV-1a/32 from
// the standard library: it is deterministic, available identically under
// the native and wasip1 Go toolchains, and pulls in no new dependency —
// which is what lets native and wasm produce byte-identical anchors. The
// surrounding protocol (alphabet, blank-line seeding, 1-byte hash mapped
// to two alphabet chars, LINE#HASH:content shape) is preserved.
//
// This package is a leaf: it imports only the standard library, so the
// wasm plugin (build-tagged wasip1, compiled standalone) can import it
// without dragging in non-wasm-safe code.
package hashline

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// nibbleStr is the 16-character hash alphabet. It deliberately excludes:
//   - hex digits A–F (so a hash never reads like a hex literal),
//   - visually confusable letters D, G, I, L, O (look like 0/6/1/1/0),
//   - the vowels A, E, I, O, U (so a hash never reads as an English word).
//
// A reference like "5#MQ" is therefore unmistakable for code, hex, or
// natural language. Index 0..15 maps to one nibble.
const nibbleStr = "ZPMQVRWSNKTXJBYH"

// dict maps a byte value (0..255) to its two-character hash: high nibble
// then low nibble through nibbleStr. Precomputed once.
var dict = func() [256]string {
	var d [256]string
	for i := 0; i < 256; i++ {
		d[i] = string(nibbleStr[i>>4]) + string(nibbleStr[i&0x0f])
	}
	return d
}()

// significant reports whether a line carries any letter or digit. Lines
// with none (blank, or pure punctuation/whitespace) are seeded with their
// line number so a file of identical blank lines doesn't collapse every
// anchor to the same hash.
func significant(line string) bool {
	for _, r := range line {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return true
		}
		// Catch non-ASCII letters/digits too (mirrors the reference's
		// Unicode \p{L}\p{N} check) without a regexp dependency.
		if r > 127 && (isUnicodeLetterOrDigit(r)) {
			return true
		}
	}
	return false
}

// isUnicodeLetterOrDigit is a cheap superset check for non-ASCII runes:
// anything outside the Latin-1 punctuation/symbol bands is treated as a
// letter or digit. Exact Unicode classification isn't required — the seed
// only needs to be deterministic and identical on both sides.
func isUnicodeLetterOrDigit(r rune) bool {
	// Latin-1 supplement punctuation/symbols sit in 0xA0..0xBF and the
	// multiply/divide signs at 0xD7/0xF7; everything else above 127 we
	// treat as significant.
	if r >= 0xA0 && r <= 0xBF {
		return false
	}
	if r == 0xD7 || r == 0xF7 {
		return false
	}
	return true
}

// LineHash returns the two-character content hash for a line at 1-indexed
// position lineNo. The line is normalized first: carriage returns stripped
// and trailing whitespace trimmed, matching the read-render normalization
// so a line hashes identically whether it came from disk (CRLF, trailing
// spaces) or from clean replacement content.
func LineHash(lineNo int, line string) string {
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.TrimRight(line, " \t\n\v\f")
	var seed uint32
	if !significant(line) {
		seed = uint32(lineNo)
	}
	h := fnv.New32a()
	// Seed by writing the 4 seed bytes first, then the line. Writing the
	// seed (vs. constructing fnv with a custom offset) keeps us on the
	// stdlib hasher unchanged and is trivially reproducible on both sides.
	var sb [4]byte
	sb[0] = byte(seed >> 24)
	sb[1] = byte(seed >> 16)
	sb[2] = byte(seed >> 8)
	sb[3] = byte(seed)
	_, _ = h.Write(sb[:])
	_, _ = h.Write([]byte(line))
	return dict[byte(h.Sum32()&0xff)]
}

// ── Read rendering ──────────────────────────────────────────────────────

// Render returns content with every line prefixed "LINE#HASH:". startLine
// is the absolute 1-indexed number of the first line of content (1 for a
// full-file read; the range start for a ranged read — so anchors stay
// absolute and survive paging). Line numbers are not zero-padded; the
// model parses on the "#" / ":" separators, not on column.
//
// content is split on "\n". A trailing newline yields a final empty
// element which is NOT emitted as its own prefixed line — matching how a
// read of "a\nb\n" shows two lines, not three.
func Render(content string, startLine int) string {
	if startLine < 1 {
		startLine = 1
	}
	lines := splitLines(content)
	var b strings.Builder
	for i, line := range lines {
		lineNo := startLine + i
		b.WriteString(fmt.Sprintf("%d#%s:%s", lineNo, LineHash(lineNo, line), line))
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// splitLines splits content into display lines: the same lines a reader
// sees. A single trailing "\n" is consumed (its trailing empty element
// dropped); interior blank lines are preserved. Empty content → no lines.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ── Anchor parsing ──────────────────────────────────────────────────────

// Anchor is a parsed LINE#HASH reference: 1-indexed line plus the
// 2-character content hash the model copied from read output.
type Anchor struct {
	Line int
	Hash string
}

// ParseAnchor parses a "LINE#HASH" reference such as "11#KT". It tolerates
// a leading display decoration (">>> ", "+", "-", surrounding spaces) and
// a trailing ":content" display suffix, both of which appear in rendered
// read / stale-anchor output the model may have copied verbatim. The line
// must be >= 1 and the hash exactly two characters drawn from the alphabet.
func ParseAnchor(ref string) (Anchor, error) {
	core := strings.TrimLeft(ref, " \t>+-")
	core = strings.TrimRight(core, " \t")

	hashIdx := strings.IndexByte(core, '#')
	if hashIdx <= 0 {
		return Anchor{}, fmt.Errorf("invalid anchor %q: expected \"LINE#HASH\" (e.g. \"5#MQ\")", ref)
	}
	numPart := strings.TrimSpace(core[:hashIdx])
	rest := strings.TrimLeft(core[hashIdx+1:], " \t")

	// Strip a trailing ":display content" suffix if present.
	if colon := strings.IndexByte(rest, ':'); colon >= 0 {
		rest = rest[:colon]
	}
	rest = strings.TrimSpace(rest)

	line, err := parseLineNo(numPart)
	if err != nil {
		return Anchor{}, fmt.Errorf("invalid anchor %q: %v", ref, err)
	}
	if line < 1 {
		return Anchor{}, fmt.Errorf("invalid anchor %q: line number must be >= 1", ref)
	}
	if len(rest) != 2 {
		return Anchor{}, fmt.Errorf("invalid anchor %q: hash must be exactly 2 characters from %s", ref, nibbleStr)
	}
	if !inAlphabet(rest) {
		return Anchor{}, fmt.Errorf("invalid anchor %q: hash uses characters outside the alphabet %s", ref, nibbleStr)
	}
	return Anchor{Line: line, Hash: rest}, nil
}

func parseLineNo(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("missing line number")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("line number must be a positive integer")
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0, fmt.Errorf("line number too large")
		}
	}
	return n, nil
}

func inAlphabet(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(nibbleStr, r) {
			return false
		}
	}
	return true
}

// ── Edit application ────────────────────────────────────────────────────

// Op enumerates the supported hashline operations.
type Op string

const (
	OpReplace Op = "replace"
	OpAppend  Op = "append"
	OpPrepend Op = "prepend"
)

// Edit is one hashline operation as received from the tool schema. Pos and
// End are raw "LINE#HASH" reference strings (End optional, range replace).
// Lines is the literal replacement/insertion content, one element per line,
// with NO display prefixes or diff markers.
type Edit struct {
	Op    Op       `json:"op"`
	Pos   string   `json:"pos,omitempty"`
	End   string   `json:"end,omitempty"`
	Lines []string `json:"lines"`
}

// StaleAnchorError signals that one or more anchors no longer match the
// file's current content — the file changed since the read. It carries
// fresh LINE#HASH anchors for the affected region so the model can retry
// without re-reading the whole file. NEVER relocate on a stale anchor.
type StaleAnchorError struct {
	// Mismatches lists each anchor whose hash didn't match, with the
	// expected (supplied) and actual (current) hashes.
	Mismatches []Mismatch
	// FreshAnchors is the rendered LINE#HASH: block around the stale
	// lines, ready to paste into a retry.
	FreshAnchors string
}

// Mismatch is a single stale anchor.
type Mismatch struct {
	Line     int
	Expected string
	Actual   string
}

func (e *StaleAnchorError) Error() string {
	refs := make([]string, len(e.Mismatches))
	for i, m := range e.Mismatches {
		refs[i] = fmt.Sprintf("%d#%s", m.Line, m.Expected)
	}
	plural := ""
	if len(e.Mismatches) != 1 {
		plural = "s"
	}
	return fmt.Sprintf("[E_STALE_ANCHOR] %d stale anchor%s (file changed since read). Stale refs: %s. Retry with the fresh anchors below; keep both endpoints for range replaces.\n%s",
		len(e.Mismatches), plural, strings.Join(refs, ", "), e.FreshAnchors)
}

// Apply applies edits to content and returns the new content. Edits are
// validated against the current content first (hash match per anchor);
// any mismatch returns a *StaleAnchorError carrying fresh anchors and
// leaves content untouched. Application is bottom-up (highest line first)
// so earlier line numbers stay valid as later edits change line counts.
//
// The literal-content guard rejects any Lines entry that looks like a
// rendered "LINE#HASH:" prefix or a diff "+/-" marker — the model must
// send literal file content, never the display form.
func Apply(content string, edits []Edit) (string, error) {
	if len(edits) == 0 {
		return content, nil
	}
	lines := splitLines(content)
	hadTrailingNewline := strings.HasSuffix(content, "\n") || content == ""

	// Parse + structurally validate every edit before mutating anything.
	parsed := make([]resolved, 0, len(edits))
	var mismatches []Mismatch
	for i, e := range edits {
		if err := assertNoDisplayPrefixes(e.Lines); err != nil {
			return content, fmt.Errorf("edit %d: %w", i, err)
		}
		var r resolved
		switch e.Op {
		case OpReplace:
			if e.Pos == "" {
				return content, fmt.Errorf("[E_BAD_OP] edit %d: op \"replace\" requires a \"pos\" anchor", i)
			}
			pos, err := ParseAnchor(e.Pos)
			if err != nil {
				return content, fmt.Errorf("edit %d: %v", i, err)
			}
			r = resolved{op: OpReplace, pos: pos, hasPos: true, lines: e.Lines}
			if e.End != "" {
				end, err := ParseAnchor(e.End)
				if err != nil {
					return content, fmt.Errorf("edit %d: %v", i, err)
				}
				if end.Line < pos.Line {
					return content, fmt.Errorf("[E_BAD_OP] edit %d: range start line %d must be <= end line %d", i, pos.Line, end.Line)
				}
				r.end = end
				r.hasEnd = true
			}
		case OpAppend, OpPrepend:
			r = resolved{op: e.Op, lines: e.Lines}
			if e.End != "" {
				return content, fmt.Errorf("[E_BAD_OP] edit %d: op %q does not support \"end\"", i, e.Op)
			}
			if e.Pos != "" {
				pos, err := ParseAnchor(e.Pos)
				if err != nil {
					return content, fmt.Errorf("edit %d: %v", i, err)
				}
				r.pos = pos
				r.hasPos = true
			}
			if len(e.Lines) == 0 {
				return content, fmt.Errorf("[E_BAD_OP] edit %d: %q with empty lines; provide content to insert", i, e.Op)
			}
		default:
			return content, fmt.Errorf("[E_BAD_OP] edit %d: unknown op %q (expected replace, append, prepend)", i, e.Op)
		}
		parsed = append(parsed, r)
	}

	// Validate anchors against current content. Collect ALL mismatches so
	// the retry block covers every stale region at once.
	seenStale := map[int]bool{}
	for _, r := range parsed {
		if r.hasPos {
			if m, ok := checkAnchor(lines, r.pos); !ok {
				if !seenStale[m.Line] {
					seenStale[m.Line] = true
					mismatches = append(mismatches, m)
				}
			}
		}
		if r.hasEnd {
			if m, ok := checkAnchor(lines, r.end); !ok {
				if !seenStale[m.Line] {
					seenStale[m.Line] = true
					mismatches = append(mismatches, m)
				}
			}
		}
	}
	if len(mismatches) > 0 {
		return content, &StaleAnchorError{
			Mismatches:   mismatches,
			FreshAnchors: freshAnchorBlock(lines, mismatches),
		}
	}

	// A non-existent anchored line is already reported as a stale anchor by
	// checkAnchor above, so every surviving anchor points at a real line.

	// Conflict check: reject overlapping replace ranges in a single request.
	if err := assertNoConflicts(parsed); err != nil {
		return content, err
	}

	// Each edit resolves to a [start,end) line
	// range to splice. Sorting by start line descending means later splices
	// never shift the line numbers an earlier (lower-line) splice still
	// depends on — line numbers stay valid as the slice mutates.
	splices := make([]splice, 0, len(parsed))
	for idx, r := range parsed {
		switch r.op {
		case OpReplace:
			start, end := r.replaceSpan()
			splices = append(splices, splice{start: start, end: end, repl: r.lines, order: idx})
		case OpAppend:
			at := len(lines)
			if r.hasPos {
				at = r.pos.Line // after the anchored line
			}
			splices = append(splices, splice{start: at, end: at, repl: r.lines, order: idx})
		case OpPrepend:
			at := 0
			if r.hasPos {
				at = r.pos.Line - 1 // before the anchored line
			}
			splices = append(splices, splice{start: at, end: at, repl: r.lines, order: idx})
		}
	}
	// Descending by start; ties broken by original order descending so a
	// pure-insertion (start==end) at the same point applies later edits
	// first, keeping the relative order of same-boundary inserts.
	sortSplicesDescending(splices)

	out := append([]string(nil), lines...)
	for _, s := range splices {
		merged := make([]string, 0, s.start+len(s.repl)+(len(out)-s.end))
		merged = append(merged, out[:s.start]...)
		merged = append(merged, s.repl...)
		merged = append(merged, out[s.end:]...)
		out = merged
	}

	result := strings.Join(out, "\n")
	if hadTrailingNewline && result != "" {
		result += "\n"
	}
	if content != "" && result == "" {
		return content, fmt.Errorf("[E_WOULD_EMPTY] refusing to empty a non-empty file through edit; use write instead")
	}
	return result, nil
}

// checkAnchor returns (mismatch, ok). ok=true when the line exists and its
// current hash equals the anchor's. A non-existent line is reported as a
// mismatch with an empty actual hash so it joins the stale-retry block.
func checkAnchor(lines []string, a Anchor) (Mismatch, bool) {
	if a.Line < 1 || a.Line > len(lines) {
		return Mismatch{Line: a.Line, Expected: a.Hash, Actual: ""}, false
	}
	actual := LineHash(a.Line, lines[a.Line-1])
	if actual == a.Hash {
		return Mismatch{}, true
	}
	return Mismatch{Line: a.Line, Expected: a.Hash, Actual: actual}, false
}

// resolved is a parsed+structurally-validated edit ready for anchor checks
// and span resolution.
type resolved struct {
	op     Op
	pos    Anchor
	hasPos bool
	end    Anchor
	hasEnd bool
	lines  []string
}

// replaceSpan returns the [start,end) 0-indexed line range a replace edit
// occupies. Only meaningful for OpReplace.
func (r resolved) replaceSpan() (int, int) {
	start := r.pos.Line - 1
	end := r.pos.Line
	if r.hasEnd {
		end = r.end.Line
	}
	return start, end
}

// assertNoConflicts rejects two replace edits whose original line ranges
// overlap — applying both bottom-up would corrupt the file. Inserts
// (append/prepend) never conflict structurally and are left alone.
func assertNoConflicts(parsed []resolved) error {
	for i := 0; i < len(parsed); i++ {
		if parsed[i].op != OpReplace {
			continue
		}
		si, ei := parsed[i].replaceSpan()
		for j := i + 1; j < len(parsed); j++ {
			if parsed[j].op != OpReplace {
				continue
			}
			sj, ej := parsed[j].replaceSpan()
			if si < ej && sj < ei {
				return fmt.Errorf("[E_EDIT_CONFLICT] edits %d and %d replace overlapping line ranges; merge them into one non-overlapping change", i, j)
			}
		}
	}
	return nil
}

// splice is one resolved character-level edit: replace lines [start,end)
// (0-indexed, end exclusive) with repl. Inserts use start==end. order is
// the original edit index, the secondary sort key.
type splice struct {
	start int
	end   int
	repl  []string
	order int
}

// sortSplicesDescending sorts splices by start line descending; ties broken
// by original edit order descending. Insertion sort keeps the package
// dependency-free and edit counts per request are tiny.
func sortSplicesDescending(items []splice) {
	for i := 1; i < len(items); i++ {
		cur := items[i]
		j := i - 1
		for j >= 0 && spliceLess(items[j], cur) {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = cur
	}
}

// spliceLess reports whether a should sort AFTER b under the descending
// order (so insertion sort moves a forward when a is "less"). a is "less"
// (sorts later) when its start is smaller, or equal start with smaller
// original order.
func spliceLess(a, b splice) bool {
	if a.start != b.start {
		return a.start < b.start
	}
	return a.order < b.order
}

// freshAnchorBlock renders a fresh LINE#HASH: block around the stale lines,
// with two lines of context on each side, marking the stale lines with
// ">>> " and context lines with "    " so the model can see exactly which
// anchors to resend.
func freshAnchorBlock(lines []string, mismatches []Mismatch) string {
	if len(lines) == 0 {
		return "(file is empty)"
	}
	display := map[int]bool{}
	stale := map[int]bool{}
	for _, m := range mismatches {
		stale[m.Line] = true
		for i := m.Line - 2; i <= m.Line+2; i++ {
			if i >= 1 && i <= len(lines) {
				display[i] = true
			}
		}
	}
	// Ordered line numbers.
	nums := make([]int, 0, len(display))
	for i := 1; i <= len(lines); i++ {
		if display[i] {
			nums = append(nums, i)
		}
	}
	var b strings.Builder
	prev := -1
	for _, n := range nums {
		if prev != -1 && n > prev+1 {
			b.WriteString("    ...\n")
		}
		prev = n
		content := lines[n-1]
		prefix := fmt.Sprintf("%d#%s:%s", n, LineHash(n, content), content)
		if stale[n] {
			b.WriteString(">>> " + prefix + "\n")
		} else {
			b.WriteString("    " + prefix + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── Literal-content guard ───────────────────────────────────────────────

// assertNoDisplayPrefixes rejects Lines entries that look like rendered
// hashline display ("LINE#HASH:" / "#HASH:") or diff markers ("+#HASH:",
// "- N    "). The model must send literal file content; silently stripping
// the prefix would corrupt legitimate content that genuinely starts that
// way, so we reject and make the model resend.
func assertNoDisplayPrefixes(lines []string) error {
	for _, line := range lines {
		if line == "" {
			continue
		}
		if hasDisplayPrefix(line) {
			return fmt.Errorf("[E_INVALID_PATCH] \"lines\" must contain literal file content, not a rendered \"LINE#HASH:\" or diff \"+/-\" prefix. Offending line: %q", line)
		}
	}
	return nil
}

// hasDisplayPrefix reports whether s begins with a hashline display prefix
// or a diff marker that the renderer (or a diff view) would have produced.
// Recognised shapes (after optional leading ">>>"/">>" and spaces):
//
//	<digits>#<HH>:        e.g. "11#KT:..."
//	#<HH>:                e.g. "#KT:..."
//	+<optional digits>#<HH>:   diff-add form
//	-<digits><4+ spaces>       diff-remove form
func hasDisplayPrefix(s string) bool {
	t := s
	// Strip a leading diff "+" (add) — that whole form is rejected too.
	plus := false
	if strings.HasPrefix(t, "+") {
		plus = true
		t = t[1:]
	}
	// Diff "- N    " remove form.
	if !plus && strings.HasPrefix(s, "-") {
		rest := s[1:]
		rest = strings.TrimLeft(rest, " \t")
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits > 0 && strings.HasPrefix(rest[digits:], "    ") {
			return true
		}
	}
	// Strip leading ">>>"/">>" decoration and whitespace.
	t = strings.TrimLeft(t, " \t")
	for strings.HasPrefix(t, ">") {
		t = strings.TrimLeft(t[1:], " \t")
	}
	// Optional leading line digits, then "#".
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	t = strings.TrimLeft(t[i:], " \t")
	if !strings.HasPrefix(t, "#") {
		return false
	}
	t = strings.TrimLeft(t[1:], " \t")
	// Exactly two alphabet chars then ":".
	if len(t) < 3 {
		return false
	}
	if !inAlphabet(t[:2]) {
		return false
	}
	return t[2] == ':'
}
