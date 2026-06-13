package vimmode

// VISUAL-mode handling: `v` enters (anchoring at the cursor); motions extend
// the live end of the selection; d/x delete it, y yanks it, c changes it. ESC
// (driven by the caller) leaves VISUAL — but for robustness the engine also
// treats an explicit "esc" key here as a return to NORMAL.

// handleVisual dispatches a key while a character-wise visual selection is
// active. The selection spans [min(anchor,cursor), max(anchor,cursor)]
// inclusive of the rune at the higher end (vim's char-wise visual is
// inclusive).
func (e *Engine) handleVisual(key, buf string, cursor int) Result {
	// Count prefix (so 3l extends the selection by three).
	if d, ok := digit(key); ok {
		if !(d == 0 && e.count == 0) {
			e.count = e.count*10 + d
			return e.result(buf, cursor, true)
		}
	}

	// g-prefix (gg) inside visual.
	if e.pendingG {
		e.pendingG = false
		if key == "g" {
			e.resetPending()
			return e.result(buf, 0, true)
		}
		e.resetPending()
		return e.result(buf, cursor, true)
	}

	switch key {
	case "esc":
		e.mode = ModeNormal
		e.resetPending()
		return e.result(buf, cursor, true)
	case "v":
		// Toggle visual off (v in visual returns to normal).
		e.mode = ModeNormal
		e.resetPending()
		return e.result(buf, cursor, true)
	case "g":
		e.pendingG = true
		return e.result(buf, cursor, true)
	case "d", "x":
		return e.visualOperate(buf, cursor, opDelete)
	case "y":
		return e.visualOperate(buf, cursor, opYank)
	case "c":
		return e.visualOperate(buf, cursor, opChange)
	}

	// Motions just move the live cursor (extending the selection); we never
	// run an operator-over-motion in visual mode.
	if mr, ok := e.motion(key, buf, cursor); ok {
		e.resetPending()
		return e.result(buf, mr.newCursor, true)
	}

	// Unknown key: consume it (don't leak into the editor while in VISUAL) and
	// clear any half-count.
	e.resetPending()
	return e.result(buf, cursor, true)
}

// visualOperate applies an operator to the current visual selection then
// returns to NORMAL (or INSERT for change). The selection is char-wise and
// inclusive of the rune at the higher end.
func (e *Engine) visualOperate(buf string, cursor int, op pendingOp) Result {
	lo, hi := e.visualAnchor, cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	hi = nextRune(buf, hi) // inclusive of the rune under the higher end
	lo = clampToRune(buf, lo)
	hi = clampToRune(buf, hi)

	cut := buf[lo:hi]
	e.setRegister(cut, false)
	e.resetPending()

	switch op {
	case opYank:
		e.mode = ModeNormal
		return e.result(buf, lo, true)
	case opChange:
		nb := buf[:lo] + buf[hi:]
		e.mode = ModeInsert
		return e.result(nb, lo, true)
	default: // delete / x
		nb := buf[:lo] + buf[hi:]
		e.mode = ModeNormal
		return e.result(nb, clampCursorInLine(nb, lo), true)
	}
}
