// Package vimmode is a PURE modal-editing engine for the chat input: no
// Bubble Tea, no TUI dependencies. It models a small, bounded subset of vim
// (NORMAL / INSERT / VISUAL modes; the motions, edits, registers, and paste
// listed in .agent/decisions/2026-06-13-keymap-phase2-modal-vim.md) over a
// single-line-or-multiline text buffer addressed by a BYTE cursor offset.
//
// The engine is a state machine driven one keystroke at a time via Handle.
// All mutation happens on the (buf, cursor) pair the caller passes in; the
// engine returns the new buffer + cursor + mode, never touching the editor
// itself. This keeps it exhaustively table-testable without a terminal.
//
// Scope is deliberately bounded (see the decision's NON-goals): no text
// objects, named registers, marks, macros, :ex commands, search, `.` repeat,
// indent, or join. The engine is rune-safe — motions and edits operate over
// runes (UTF-8), never splitting a multibyte rune.
package vimmode

// Mode is the editing mode the engine is currently in.
type Mode int

const (
	// ModeInsert is ordinary typing — the engine does not intercept keys in
	// this mode except ESC (handled by the caller's routing, which flips
	// INSERT->NORMAL). The TUI starts here when vim is enabled so the user can
	// type immediately rather than being trapped in NORMAL on launch.
	ModeInsert Mode = iota
	// ModeNormal is command mode: motions move the cursor, operators + motions
	// edit, etc. ESC in NORMAL is a no-op (per the decision).
	ModeNormal
	// ModeVisual is character-wise visual selection: motions extend the
	// selection from a fixed anchor; d/x/y/c act on the selection.
	ModeVisual
)

// String renders the mode for the status indicator.
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "NORMAL"
	case ModeVisual:
		return "VISUAL"
	default:
		return "INSERT"
	}
}

// Result is what Handle returns: the (possibly mutated) buffer + cursor, the
// mode after the keystroke, and whether the engine consumed the key. When
// Consumed is false the caller falls through to its normal key handling (the
// engine ignores keys it doesn't recognise in NORMAL/VISUAL so the caller can
// still dispatch global chords). NewBuf/NewCursor are always valid: an
// unconsumed key returns the buffer + cursor unchanged.
type Result struct {
	NewBuf    string
	NewCursor int // byte offset into NewBuf
	Mode      Mode
	Consumed  bool
}

// pendingOp is the operator awaiting a motion (d/c/y), or none.
type pendingOp int

const (
	opNone pendingOp = iota
	opDelete
	opChange
	opYank
)

// Engine holds the modal state that persists across keystrokes: the current
// mode, the single unnamed register (with its line-wise flag), and the
// in-progress count / operator / replace / g-prefix / visual-anchor state.
//
// The zero value starts in INSERT mode with an empty register and no pending
// state, which is the correct launch posture.
type Engine struct {
	mode Mode

	// register is the single unnamed register, fed by x / d* / y* and pasted
	// by p / P. regLinewise tracks whether it was yanked/deleted line-wise (dd,
	// yy, cc) so paste re-inserts it as whole lines rather than inline.
	register    string
	regLinewise bool

	// count accumulates a numeric prefix ("3" then "j" => count 3). 0 means
	// "no count given" (treated as 1 by motions/operators).
	count int

	// op is the pending operator (d/c/y) awaiting a motion; opNone otherwise.
	op pendingOp
	// opCount is the count captured BEFORE the operator (e.g. the 2 in "2dw");
	// multiplied with any post-operator count.
	opCount int

	// pendingG is set after the first 'g' (awaiting the second 'g' for gg).
	pendingG bool
	// pendingR is set after 'r' (awaiting the replacement character).
	pendingR bool

	// visualAnchor is the byte offset the visual selection is anchored at
	// (the position where 'v' was pressed). The other end is the live cursor.
	visualAnchor int
}

// New returns an engine in INSERT mode (the launch posture when vim is
// enabled).
func New() *Engine {
	return &Engine{mode: ModeInsert}
}

// Mode reports the current mode (for the status indicator + routing).
func (e *Engine) Mode() Mode { return e.mode }

// SetMode forces the mode. The caller uses this for the two transitions it
// owns rather than the engine: ESC in INSERT -> NORMAL (the vim-schema-only
// remap), and the reset-to-INSERT after submitting a prompt. Switching out of
// VISUAL or to INSERT clears any half-entered pending state so a stale count /
// operator can't leak across the transition.
func (e *Engine) SetMode(m Mode) {
	e.mode = m
	e.resetPending()
}

// resetPending clears the count / operator / g / r transient state. Called
// after every completed command and on mode changes the caller drives.
func (e *Engine) resetPending() {
	e.count = 0
	e.op = opNone
	e.opCount = 0
	e.pendingG = false
	e.pendingR = false
}

// effectiveCount returns the accumulated count, defaulting to 1.
func (e *Engine) effectiveCount() int {
	if e.count <= 0 {
		return 1
	}
	return e.count
}

// Handle processes one keystroke against (buf, cursor) and returns the result.
// cursor is a byte offset into buf; NewCursor is a byte offset into NewBuf. The
// engine only acts in NORMAL and VISUAL modes — in INSERT it returns
// Consumed=false so the caller routes the key to the editor as usual.
func (e *Engine) Handle(key, buf string, cursor int) Result {
	cursor = clampToRune(buf, cursor)
	switch e.mode {
	case ModeNormal:
		return e.handleNormal(key, buf, cursor)
	case ModeVisual:
		return e.handleVisual(key, buf, cursor)
	default:
		// INSERT — engine doesn't intercept.
		return Result{NewBuf: buf, NewCursor: cursor, Mode: e.mode, Consumed: false}
	}
}

// result is a small helper for the common "buffer + cursor + current mode,
// consumed" return.
func (e *Engine) result(buf string, cursor int, consumed bool) Result {
	return Result{NewBuf: buf, NewCursor: clampToRune(buf, cursor), Mode: e.mode, Consumed: consumed}
}

// handleNormal dispatches a key in NORMAL mode.
func (e *Engine) handleNormal(key, buf string, cursor int) Result {
	// Replace pending (after 'r'): the next single-rune key replaces the char
	// under the cursor.
	if e.pendingR {
		e.pendingR = false
		if r, ok := singleRune(key); ok {
			nb, nc := replaceCharAt(buf, cursor, r)
			e.resetPending()
			return e.result(nb, nc, true)
		}
		// Non-character key (esc, etc.) cancels the replace.
		e.resetPending()
		return e.result(buf, cursor, true)
	}

	// g-prefix pending (after first 'g'): only 'g' completes it (gg).
	if e.pendingG {
		e.pendingG = false
		if key == "g" {
			cnt := e.count // gg with a count is "go to line N"; we model a
			// single-line buffer mostly, but support multiline: count = line.
			e.resetPending()
			if cnt > 0 {
				return e.finishMotionOrOp(buf, cursor, gotoLine(buf, cnt))
			}
			return e.finishMotionOrOp(buf, cursor, motionResult{newCursor: 0, linewise: true, inclusive: false})
		}
		// Unknown g-combo: cancel.
		e.resetPending()
		return e.result(buf, cursor, true)
	}

	// Numeric count prefix. A leading '0' is the line-start motion, not a
	// count digit; only 1-9 start a count, and 0-9 extend an existing one.
	if d, ok := digit(key); ok {
		if !(d == 0 && e.count == 0) {
			e.count = e.count*10 + d
			return e.result(buf, cursor, true)
		}
	}

	switch key {
	// --- mode entry ---
	case "i":
		e.enterInsert()
		return e.result(buf, cursor, true)
	case "a":
		nc := nextRune(buf, cursor)
		e.enterInsert()
		return e.result(buf, nc, true)
	case "I":
		e.enterInsert()
		return e.result(buf, lineStart(buf, cursor), true)
	case "A":
		e.enterInsert()
		return e.result(buf, lineEnd(buf, cursor), true)
	case "o":
		nb, nc := openLineBelow(buf, cursor)
		e.enterInsert()
		return e.result(nb, nc, true)
	case "O":
		nb, nc := openLineAbove(buf, cursor)
		e.enterInsert()
		return e.result(nb, nc, true)
	case "v":
		e.mode = ModeVisual
		e.visualAnchor = cursor
		e.resetPending()
		return e.result(buf, cursor, true)

	// --- operators (await a motion) ---
	case "d", "c", "y":
		return e.beginOperator(key, buf, cursor)

	// --- one-shot edits ---
	case "x":
		return e.deleteCharUnderCursor(buf, cursor)
	case "D":
		return e.deleteToLineEnd(buf, cursor, false)
	case "C":
		return e.deleteToLineEnd(buf, cursor, true)
	case "s":
		return e.substituteChar(buf, cursor)
	case "r":
		e.pendingR = true
		return e.result(buf, cursor, true)
	case "p":
		return e.paste(buf, cursor, true)
	case "P":
		return e.paste(buf, cursor, false)

	// --- prefixes ---
	case "g":
		e.pendingG = true
		return e.result(buf, cursor, true)
	}

	// Motions (h j k l w b e 0 ^ $ G) — when an operator is pending, the motion
	// defines the operated range; otherwise it just moves the cursor.
	if mr, ok := e.motion(key, buf, cursor); ok {
		return e.finishMotionOrOp(buf, cursor, mr)
	}

	// Unrecognised NORMAL-mode key. Clear any half-entered count/operator so it
	// can't leak, and DO consume it: in NORMAL mode stray letters must not fall
	// through to the editor and get typed into the buffer. (The caller still
	// owns ESC and global chords, which it checks before routing here.)
	e.resetPending()
	return e.result(buf, cursor, true)
}

// enterInsert switches to INSERT and clears pending state.
func (e *Engine) enterInsert() {
	e.mode = ModeInsert
	e.resetPending()
}

// beginOperator records a pending operator (d/c/y), folding any count entered
// before it into opCount. A doubled operator (dd/cc/yy) is line-wise and is
// detected on the next keystroke in finishOperatorDouble.
func (e *Engine) beginOperator(key, buf string, cursor int) Result {
	var op pendingOp
	switch key {
	case "d":
		op = opDelete
	case "c":
		op = opChange
	case "y":
		op = opYank
	}
	// If the SAME operator is already pending, this is the doubled (line-wise)
	// form: dd / cc / yy.
	if e.op == op {
		return e.operateLinewise(buf, cursor)
	}
	// A different operator was pending (e.g. "dy") — abandon the first.
	e.op = op
	e.opCount = e.count
	e.count = 0
	return e.result(buf, cursor, true)
}

// motionResult describes where a motion lands and how an operator over it
// behaves: linewise motions operate on whole lines; inclusive motions include
// the rune at the landing cursor in the operated range (e.g. 'e', '$').
type motionResult struct {
	newCursor int
	linewise  bool
	inclusive bool
}

// motion computes a motion result for the given key, applying the engine's
// effective count. ok=false means the key isn't a motion. When an operator is
// pending, the count entered before the operator (opCount) multiplies the
// count entered after it — so "2dw" and "d2w" both delete two words.
func (e *Engine) motion(key, buf string, cursor int) (motionResult, bool) {
	n := e.effectiveCount()
	if e.op != opNone && e.opCount > 0 {
		n *= e.opCount
	}
	switch key {
	case "h":
		c := cursor
		for i := 0; i < n; i++ {
			c = prevRuneSameLine(buf, c)
		}
		return motionResult{newCursor: c}, true
	case "l":
		c := cursor
		for i := 0; i < n; i++ {
			c = nextRuneSameLine(buf, c)
		}
		return motionResult{newCursor: c}, true
	case "j":
		return motionResult{newCursor: moveVertical(buf, cursor, n), linewise: true}, true
	case "k":
		return motionResult{newCursor: moveVertical(buf, cursor, -n), linewise: true}, true
	case "w":
		c := cursor
		for i := 0; i < n; i++ {
			c = wordForward(buf, c)
		}
		return motionResult{newCursor: c}, true
	case "b":
		c := cursor
		for i := 0; i < n; i++ {
			c = wordBackward(buf, c)
		}
		return motionResult{newCursor: c}, true
	case "e":
		c := cursor
		for i := 0; i < n; i++ {
			c = wordEnd(buf, c)
		}
		return motionResult{newCursor: c, inclusive: true}, true
	case "0":
		return motionResult{newCursor: lineStart(buf, cursor)}, true
	case "^":
		return motionResult{newCursor: lineFirstNonBlank(buf, cursor)}, true
	case "$":
		return motionResult{newCursor: lineEnd(buf, cursor), inclusive: true}, true
	case "G":
		if e.count > 0 {
			return gotoLine(buf, e.count), true
		}
		return motionResult{newCursor: lastLineStart(buf), linewise: true}, true
	}
	return motionResult{}, false
}

// finishMotionOrOp applies a motion result: if an operator is pending it
// operates over [cursor, motion] (respecting line-wise / inclusive); otherwise
// it just moves the cursor. Either way it clears pending state.
func (e *Engine) finishMotionOrOp(buf string, cursor int, mr motionResult) Result {
	if e.op != opNone {
		return e.applyOperator(buf, cursor, mr)
	}
	e.resetPending()
	// As a bare cursor move, an inclusive char-wise motion (e.g. `$`, `e`)
	// lands ON the last rune, not past it — clamp so the NORMAL cursor never
	// rests on the newline / EOF. Line-wise motions already land on a line
	// start and are fine as-is.
	dst := mr.newCursor
	if mr.inclusive && !mr.linewise {
		dst = clampCursorInLine(buf, dst)
	}
	return e.result(buf, dst, true)
}

// applyOperator deletes/changes/yanks the text between the cursor and the
// motion target, honouring line-wise and inclusive semantics, then clears the
// pending operator. Change leaves the engine in INSERT at the cut point.
func (e *Engine) applyOperator(buf string, cursor int, mr motionResult) Result {
	op := e.op
	if mr.linewise {
		return e.operateLinewiseSpan(buf, cursor, mr.newCursor, op)
	}

	lo, hi := cursor, mr.newCursor
	if lo > hi {
		lo, hi = hi, lo
	}
	if mr.inclusive {
		// Include the rune at the (higher) landing position.
		hi = nextRune(buf, hi)
	}
	lo = clampToRune(buf, lo)
	hi = clampToRune(buf, hi)

	cut := buf[lo:hi]
	nb := buf[:lo] + buf[hi:]
	e.setRegister(cut, false)
	e.resetPending()

	switch op {
	case opChange:
		// Change leaves the cursor at the cut point in INSERT — no in-line
		// clamp (INSERT can rest at EOL).
		e.mode = ModeInsert
		return e.result(nb, lo, true)
	case opYank:
		// Yank doesn't modify the buffer; cursor goes to range start.
		e.mode = ModeNormal
		return e.result(buf, lo, true)
	default: // delete
		e.mode = ModeNormal
		return e.result(nb, clampCursorInLine(nb, lo), true)
	}
}

// operateLinewise handles the doubled operator (dd/cc/yy) on the current line,
// applying the effective count for multiple lines.
func (e *Engine) operateLinewise(buf string, cursor int) Result {
	op := e.op
	n := e.effectiveCount()
	if e.opCount > 0 {
		n = e.opCount * n
	}
	startLine := lineStart(buf, cursor)
	// Span n lines downward.
	end := startLine
	for i := 0; i < n; i++ {
		end = lineEndWithNewline(buf, end)
	}
	text := buf[startLine:end]
	e.setRegister(text, true)
	e.resetPending()

	switch op {
	case opYank:
		e.mode = ModeNormal
		return e.result(buf, startLine, true)
	case opChange:
		// cc deletes the line content but KEEPS the line (no newline removed)
		// and enters INSERT at the line start. Model: remove the text up to but
		// not including the trailing newline of the last spanned line.
		contentEnd := end
		if contentEnd > startLine && buf[contentEnd-1] == '\n' {
			contentEnd--
		}
		nb := buf[:startLine] + buf[contentEnd:]
		// Register for cc is the line content WITH a trailing newline (vim
		// stores cc/dd line-wise the same way); keep as set above.
		e.mode = ModeInsert
		return e.result(nb, startLine, true)
	default: // dd
		nb := buf[:startLine] + buf[end:]
		e.mode = ModeNormal
		// Cursor lands at the start of what is now the line at startLine.
		return e.result(nb, clampToRune(nb, startLine), true)
	}
}

// operateLinewiseSpan handles an operator over a line-wise motion (e.g. dj,
// d2j, yk, cG, dgg): operate on every whole line spanned between the cursor's
// line and the motion target's line, inclusive of both. Mirrors dd/cc/yy
// semantics for the multi-line case.
func (e *Engine) operateLinewiseSpan(buf string, cursor, target int, op pendingOp) Result {
	startLine := lineStart(buf, cursor)
	targetLine := lineStart(buf, target)
	lo, hi := startLine, targetLine
	if lo > hi {
		lo, hi = hi, lo
	}
	// Extend hi to the end of its line, including the trailing newline so the
	// whole line is removed.
	end := lineEndWithNewline(buf, hi)
	text := buf[lo:end]
	e.setRegister(text, true)
	e.resetPending()

	switch op {
	case opYank:
		e.mode = ModeNormal
		return e.result(buf, lo, true)
	case opChange:
		contentEnd := end
		if contentEnd > lo && buf[contentEnd-1] == '\n' {
			contentEnd--
		}
		nb := buf[:lo] + buf[contentEnd:]
		e.mode = ModeInsert
		return e.result(nb, lo, true)
	default: // delete
		nb := buf[:lo] + buf[end:]
		e.mode = ModeNormal
		return e.result(nb, clampToRune(nb, lo), true)
	}
}
