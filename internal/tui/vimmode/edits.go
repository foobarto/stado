package vimmode

// One-shot edits (x / D / C / s / r), paste (p / P), and register handling.
// These complement the operator+motion machinery in vimmode.go.

// setRegister stores text in the single unnamed register. linewise records
// whether it was a line-wise yank/delete (dd/cc/yy or a line-wise operator
// span) so paste re-inserts it as whole lines.
func (e *Engine) setRegister(text string, linewise bool) {
	e.register = text
	e.regLinewise = linewise
}

// deleteCharUnderCursor implements `x`: delete the rune under the cursor (and
// the next count-1 runes), saving them to the register char-wise. The cursor
// stays put, snapping back one rune if it deleted the last char on the line.
func (e *Engine) deleteCharUnderCursor(buf string, cursor int) Result {
	n := e.effectiveCount()
	// Advance the deletion end past up to n runes, but never across a newline —
	// `x` only deletes within the current line.
	end := cursor
	for i := 0; i < n; i++ {
		if end >= len(buf) || buf[end] == '\n' {
			break
		}
		ne := nextRune(buf, end)
		if ne == end {
			break
		}
		end = ne
	}
	if end <= cursor {
		e.resetPending()
		return e.result(buf, cursor, true)
	}
	cut := buf[cursor:end]
	nb := buf[:cursor] + buf[end:]
	e.setRegister(cut, false)
	e.resetPending()
	// Cursor stays at the same offset, but clamp so it doesn't sit past the
	// (now shorter) line's last rune.
	nc := clampCursorInLine(nb, cursor)
	return e.result(nb, nc, true)
}

// deleteToLineEnd implements `D` (change=false) and `C` (change=true): delete
// from the cursor to the end of the line (char-wise), saving to the register.
// C enters INSERT at the cut point.
func (e *Engine) deleteToLineEnd(buf string, cursor int, change bool) Result {
	end := lineEnd(buf, cursor)
	cut := buf[cursor:end]
	nb := buf[:cursor] + buf[end:]
	e.setRegister(cut, false)
	e.resetPending()
	if change {
		e.mode = ModeInsert
		return e.result(nb, cursor, true)
	}
	e.mode = ModeNormal
	return e.result(nb, clampCursorInLine(nb, cursor), true)
}

// substituteChar implements `s`: delete the rune under the cursor (count runes)
// and enter INSERT — equivalent to `x` then `i`.
func (e *Engine) substituteChar(buf string, cursor int) Result {
	res := e.deleteCharUnderCursor(buf, cursor)
	e.mode = ModeInsert
	res.Mode = ModeInsert
	return res
}

// paste implements `p` (after=true) / `P` (after=false). Char-wise content is
// inserted at the cursor (p inserts AFTER the cursor rune, P before). Line-wise
// content is inserted as whole lines below (p) or above (P) the current line.
func (e *Engine) paste(buf string, cursor int, after bool) Result {
	if e.register == "" {
		e.resetPending()
		return e.result(buf, cursor, true)
	}
	n := e.effectiveCount()
	text := repeat(e.register, n)

	if e.regLinewise {
		return e.pasteLinewise(buf, cursor, text, after)
	}
	// Char-wise paste.
	at := cursor
	if after {
		at = nextRune(buf, cursor)
		// p on an empty line / at EOL inserts right after the last rune.
		if at > len(buf) {
			at = len(buf)
		}
	}
	at = clampToRune(buf, at)
	nb := buf[:at] + text + buf[at:]
	e.resetPending()
	e.mode = ModeNormal
	// Cursor lands on the last pasted rune (vim parks there for char-wise p).
	nc := at + len(text)
	nc = prevRune(nb, nc)
	if nc < at {
		nc = at
	}
	return e.result(nb, nc, true)
}

// pasteLinewise inserts the register's line content below (after) or above
// (!after) the current line. The register text already carries its own
// trailing newline (set by dd/yy/cc), so we splice it at a line boundary.
func (e *Engine) pasteLinewise(buf string, cursor int, text string, after bool) Result {
	// Normalise the block to exactly one trailing newline so it always lands
	// as whole line(s). A yy on the last line captures no trailing newline, so
	// the raw register can be unterminated.
	block := trimTrailingNewline(text) + "\n"
	e.resetPending()
	e.mode = ModeNormal

	if after {
		at := lineEndWithNewline(buf, cursor)
		if at == len(buf) && (len(buf) == 0 || buf[len(buf)-1] != '\n') {
			// Current line is the last line with no trailing newline: insert a
			// separating newline, then the block content (without its own
			// trailing newline, to avoid leaving a blank final line).
			ins := "\n" + trimTrailingNewline(block)
			nb := buf + ins
			return e.result(nb, clampToRune(nb, len(buf)+1), true)
		}
		nb := buf[:at] + block + buf[at:]
		return e.result(nb, clampToRune(nb, at), true)
	}
	// Paste above: splice the block at the current line's start.
	at := lineStart(buf, cursor)
	nb := buf[:at] + block + buf[at:]
	return e.result(nb, clampToRune(nb, at), true)
}
