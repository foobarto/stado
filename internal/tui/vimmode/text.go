package vimmode

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pure byte-offset/rune helpers over a UTF-8 buffer. Every offset returned is
// guaranteed to sit on a rune boundary in [0, len(buf)]. None of these split a
// multibyte rune.

// clampToRune clamps off to [0, len(buf)] and snaps it back to the start of the
// rune it lands inside (so a mid-rune byte offset never escapes).
func clampToRune(buf string, off int) int {
	if off <= 0 {
		return 0
	}
	if off >= len(buf) {
		return len(buf)
	}
	for off > 0 && !utf8.RuneStart(buf[off]) {
		off--
	}
	return off
}

// nextRune returns the byte offset one rune forward from off, clamped to
// len(buf). Crosses newlines (unlike nextRuneSameLine).
func nextRune(buf string, off int) int {
	off = clampToRune(buf, off)
	if off >= len(buf) {
		return len(buf)
	}
	_, sz := utf8.DecodeRuneInString(buf[off:])
	return off + sz
}

// prevRune returns the byte offset one rune backward from off, clamped to 0.
func prevRune(buf string, off int) int {
	off = clampToRune(buf, off)
	if off <= 0 {
		return 0
	}
	_, sz := utf8.DecodeLastRuneInString(buf[:off])
	return off - sz
}

// nextRuneSameLine moves one rune forward but never onto or past the line's
// terminating newline — in NORMAL mode the cursor rests ON the last rune of a
// line, not after it. Returns off unchanged if already at the last rune.
func nextRuneSameLine(buf string, off int) int {
	off = clampToRune(buf, off)
	if off >= len(buf) || buf[off] == '\n' {
		return off
	}
	nr := nextRune(buf, off)
	// If the next rune is the newline (or EOF), stay put — NORMAL cursor
	// can't sit on the newline.
	if nr >= len(buf) || buf[nr] == '\n' {
		return off
	}
	return nr
}

// prevRuneSameLine moves one rune backward but not before the line start.
func prevRuneSameLine(buf string, off int) int {
	off = clampToRune(buf, off)
	ls := lineStart(buf, off)
	if off <= ls {
		return ls
	}
	return prevRune(buf, off)
}

// lineStart returns the byte offset of the first rune on the line containing
// off (the offset just after the preceding newline, or 0).
func lineStart(buf string, off int) int {
	off = clampToRune(buf, off)
	if i := strings.LastIndexByte(buf[:off], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// lineEnd returns the byte offset of the line's terminating newline (or
// len(buf) for the last line) for the line containing off.
func lineEnd(buf string, off int) int {
	off = clampToRune(buf, off)
	if i := strings.IndexByte(buf[off:], '\n'); i >= 0 {
		return off + i
	}
	return len(buf)
}

// lineEndWithNewline returns the offset just past the line's terminating
// newline (or len(buf) for the last line) for the line containing off — i.e.
// the start of the next line. Used to span whole lines for dd/yy/cc.
func lineEndWithNewline(buf string, off int) int {
	le := lineEnd(buf, off)
	if le < len(buf) && buf[le] == '\n' {
		return le + 1
	}
	return le
}

// lineLastChar returns the offset of the LAST rune on off's line — the rune
// before the terminating newline (or before EOF on the final line) — or the
// line start for an empty line. `$` uses this (inclusive) so that as an
// operator motion the inclusive +1 lands ON the newline (exclusive slice end):
// d$/c$/y$ then stop at the line end without eating the newline on a multiline
// buffer, and the bare cursor still rests on the last rune.
func lineLastChar(buf string, off int) int {
	ls := lineStart(buf, off)
	le := lineEnd(buf, off)
	if le <= ls {
		return ls // empty line
	}
	return prevRune(buf, le)
}

// lineFirstNonBlank returns the offset of the first non-whitespace rune on the
// line containing off (the `^` motion). Falls back to the line start if the
// line is all blanks.
func lineFirstNonBlank(buf string, off int) int {
	ls := lineStart(buf, off)
	le := lineEnd(buf, off)
	i := ls
	for i < le {
		r, sz := utf8.DecodeRuneInString(buf[i:])
		if !unicode.IsSpace(r) {
			return i
		}
		i += sz
	}
	return ls
}

// lastLineStart returns the start offset of the final line (the `G` target).
func lastLineStart(buf string) int {
	if len(buf) == 0 {
		return 0
	}
	// If the buffer ends with a newline, the "last line" is empty after it.
	if buf[len(buf)-1] == '\n' {
		return len(buf)
	}
	if i := strings.LastIndexByte(buf, '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// gotoLine returns a line-wise motion result to the start of the 1-based line
// number n (the `NG` / `Ngg` target), clamped to the last line.
func gotoLine(buf string, n int) motionResult {
	if n <= 1 {
		return motionResult{newCursor: 0, linewise: true}
	}
	off := 0
	line := 1
	for line < n {
		nl := strings.IndexByte(buf[off:], '\n')
		if nl < 0 {
			off = lastLineStart(buf)
			break
		}
		off += nl + 1
		line++
		if off >= len(buf) {
			break
		}
	}
	return motionResult{newCursor: clampToRune(buf, off), linewise: true}
}

// moveVertical moves the cursor delta logical lines (negative = up), trying to
// preserve the rune column within the line. Used by j/k.
func moveVertical(buf string, off, delta int) int {
	off = clampToRune(buf, off)
	col := runeColumn(buf, off)
	ls := lineStart(buf, off)
	cur := ls
	switch {
	case delta > 0:
		for i := 0; i < delta; i++ {
			ne := lineEndWithNewline(buf, cur)
			if ne >= len(buf) {
				// cur is the last line; can't go down.
				return offsetAtColumn(buf, cur, col)
			}
			cur = ne
		}
	case delta < 0:
		for i := 0; i < -delta; i++ {
			if cur == 0 {
				return offsetAtColumn(buf, 0, col)
			}
			cur = lineStart(buf, prevRune(buf, cur))
		}
	}
	return offsetAtColumn(buf, cur, col)
}

// runeColumn returns the rune index of off within its line (0-based).
func runeColumn(buf string, off int) int {
	ls := lineStart(buf, off)
	col := 0
	for i := ls; i < off; {
		_, sz := utf8.DecodeRuneInString(buf[i:])
		i += sz
		col++
	}
	return col
}

// offsetAtColumn returns the byte offset of the given rune column on the line
// starting at lineStartOff, clamped to the line's last rune.
func offsetAtColumn(buf string, lineStartOff, col int) int {
	le := lineEnd(buf, lineStartOff)
	i := lineStartOff
	c := 0
	for i < le && c < col {
		_, sz := utf8.DecodeRuneInString(buf[i:])
		i += sz
		c++
	}
	return i
}

// clampCursorInLine clamps off so it rests on a rune of its line and not on the
// terminating newline (NORMAL cursor invariant). If the line is empty the
// cursor sits at the line start.
func clampCursorInLine(buf string, off int) int {
	off = clampToRune(buf, off)
	ls := lineStart(buf, off)
	le := lineEnd(buf, off)
	if off > le {
		off = le
	}
	if off < ls {
		off = ls
	}
	// If off sits on the newline (== le) and the line is non-empty, step back
	// onto the last rune.
	if off == le && le > ls {
		return prevRune(buf, off)
	}
	return off
}

// --- word motions (vim "word" = run of word-chars OR run of punctuation,
// separated by whitespace; vim's small-word `w`/`b`/`e`). ---

type charClass int

const (
	classBlank charClass = iota
	classWord
	classPunct
)

func classify(r rune) charClass {
	switch {
	case unicode.IsSpace(r):
		return classBlank
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return classWord
	default:
		return classPunct
	}
}

func runeAt(buf string, off int) (rune, bool) {
	if off < 0 || off >= len(buf) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(buf[off:])
	return r, true
}

// wordForward implements `w`: move to the start of the next word. Newlines are
// treated as blanks, so `w` crosses lines (matching vim).
func wordForward(buf string, off int) int {
	off = clampToRune(buf, off)
	if off >= len(buf) {
		return len(buf)
	}
	r, _ := runeAt(buf, off)
	startClass := classify(r)
	// Skip the rest of the current word (same non-blank class).
	if startClass != classBlank {
		for off < len(buf) {
			r, _ := runeAt(buf, off)
			if classify(r) != startClass {
				break
			}
			off = nextRune(buf, off)
		}
	}
	// Skip blanks to the next word start.
	for off < len(buf) {
		r, _ := runeAt(buf, off)
		if classify(r) != classBlank {
			break
		}
		off = nextRune(buf, off)
	}
	return off
}

// wordBackward implements `b`: move to the start of the current or previous
// word.
func wordBackward(buf string, off int) int {
	off = clampToRune(buf, off)
	if off <= 0 {
		return 0
	}
	// Step back one and skip blanks.
	off = prevRune(buf, off)
	for off > 0 {
		r, _ := runeAt(buf, off)
		if classify(r) != classBlank {
			break
		}
		off = prevRune(buf, off)
	}
	// Now at the last rune of the target word; walk back to its start.
	r, _ := runeAt(buf, off)
	cls := classify(r)
	if cls == classBlank {
		return off
	}
	for off > 0 {
		p := prevRune(buf, off)
		pr, _ := runeAt(buf, p)
		if classify(pr) != cls {
			break
		}
		off = p
	}
	return off
}

// wordEnd implements `e`: move to the end (last rune) of the current or next
// word. This motion is inclusive when used with an operator.
func wordEnd(buf string, off int) int {
	off = clampToRune(buf, off)
	if off >= len(buf) {
		return off
	}
	// Move forward at least one rune.
	off = nextRune(buf, off)
	// Skip blanks.
	for off < len(buf) {
		r, _ := runeAt(buf, off)
		if classify(r) != classBlank {
			break
		}
		off = nextRune(buf, off)
	}
	if off >= len(buf) {
		return prevRune(buf, len(buf))
	}
	// Walk to the end of this word (last rune of the same class).
	r, _ := runeAt(buf, off)
	cls := classify(r)
	for {
		n := nextRune(buf, off)
		nr, ok := runeAt(buf, n)
		if !ok || classify(nr) != cls {
			break
		}
		off = n
	}
	return off
}

// --- small misc helpers ---

// replaceCharAt replaces the rune under the cursor with r, leaving the cursor
// on the replaced rune. If the cursor is at EOL/EOF (no rune under it), the
// buffer is unchanged.
func replaceCharAt(buf string, cursor int, r rune) (string, int) {
	cursor = clampToRune(buf, cursor)
	if cursor >= len(buf) || buf[cursor] == '\n' {
		return buf, cursor
	}
	end := nextRune(buf, cursor)
	return buf[:cursor] + string(r) + buf[end:], cursor
}

// openLineBelow inserts a newline after the current line and returns the buffer
// + the cursor at the start of the new (empty) line. Implements `o`.
func openLineBelow(buf string, cursor int) (string, int) {
	at := lineEnd(buf, cursor)
	nb := buf[:at] + "\n" + buf[at:]
	return nb, at + 1
}

// openLineAbove inserts a newline before the current line and returns the
// buffer + the cursor at the start of the new (empty) line. Implements `O`.
func openLineAbove(buf string, cursor int) (string, int) {
	at := lineStart(buf, cursor)
	nb := buf[:at] + "\n" + buf[at:]
	return nb, at
}

// digit parses a single-digit key ("0".."9").
func digit(key string) (int, bool) {
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		return int(key[0] - '0'), true
	}
	return 0, false
}

// singleRune returns the single rune of key if key is exactly one rune (used by
// `r<char>`); ok=false for chord keys like "esc", "ctrl+a", "enter".
func singleRune(key string) (rune, bool) {
	if utf8.RuneCountInString(key) != 1 {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(key)
	return r, true
}

// repeat returns s concatenated n times (n<=1 returns s).
func repeat(s string, n int) string {
	if n <= 1 {
		return s
	}
	return strings.Repeat(s, n)
}

// trimTrailingNewline drops a single trailing '\n' if present.
func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}
