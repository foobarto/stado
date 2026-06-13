package vimmode

import "testing"

// step is one keystroke in a test case's key sequence.
type tc struct {
	name      string
	startBuf  string
	startCur  int
	startMode Mode     // mode the engine begins the sequence in
	anchor    int      // visual anchor, used only when startMode==ModeVisual
	keys      []string // keystrokes to feed
	wantBuf   string
	wantCur   int
	wantMode  Mode
	wantReg   string // expected register content after the sequence (""=skip check)
	wantLine  *bool  // expected regLinewise (nil=skip check)
}

func ptrBool(b bool) *bool { return &b }

func runCase(t *testing.T, c tc) {
	t.Helper()
	e := New()
	e.mode = c.startMode
	if c.startMode == ModeVisual {
		e.visualAnchor = c.anchor
	}
	buf, cur := c.startBuf, c.startCur
	var res Result
	for _, k := range c.keys {
		res = e.Handle(k, buf, cur)
		buf, cur = res.NewBuf, res.NewCursor
	}
	if buf != c.wantBuf {
		t.Errorf("buf = %q, want %q", buf, c.wantBuf)
	}
	if cur != c.wantCur {
		t.Errorf("cursor = %d, want %d (buf=%q)", cur, c.wantCur, buf)
	}
	if e.mode != c.wantMode {
		t.Errorf("mode = %v, want %v", e.mode, c.wantMode)
	}
	if c.wantReg != "" && e.register != c.wantReg {
		t.Errorf("register = %q, want %q", e.register, c.wantReg)
	}
	if c.wantLine != nil && e.regLinewise != *c.wantLine {
		t.Errorf("regLinewise = %v, want %v", e.regLinewise, *c.wantLine)
	}
}

// --- motions (basic + counts) ---

func TestMotions(t *testing.T) {
	cases := []tc{
		{name: "l moves right one rune", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"l"}, wantBuf: "hello", wantCur: 1, wantMode: ModeNormal},
		{name: "h moves left one rune", startBuf: "hello", startCur: 3, startMode: ModeNormal,
			keys: []string{"h"}, wantBuf: "hello", wantCur: 2, wantMode: ModeNormal},
		{name: "h at line start stays", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"h"}, wantBuf: "hello", wantCur: 0, wantMode: ModeNormal},
		{name: "l at last rune stays (normal cursor can't pass EOL)", startBuf: "hi", startCur: 1, startMode: ModeNormal,
			keys: []string{"l"}, wantBuf: "hi", wantCur: 1, wantMode: ModeNormal},
		{name: "3l moves right three", startBuf: "abcdef", startCur: 0, startMode: ModeNormal,
			keys: []string{"3", "l"}, wantBuf: "abcdef", wantCur: 3, wantMode: ModeNormal},
		{name: "0 to line start", startBuf: "hello", startCur: 4, startMode: ModeNormal,
			keys: []string{"0"}, wantBuf: "hello", wantCur: 0, wantMode: ModeNormal},
		{name: "$ to line end (last rune)", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"$"}, wantBuf: "hello", wantCur: 4, wantMode: ModeNormal},
		{name: "^ to first non-blank", startBuf: "  hi", startCur: 3, startMode: ModeNormal,
			keys: []string{"^"}, wantBuf: "  hi", wantCur: 2, wantMode: ModeNormal},
		{name: "w to next word", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"w"}, wantBuf: "foo bar", wantCur: 4, wantMode: ModeNormal},
		{name: "2w skips two words", startBuf: "a b c", startCur: 0, startMode: ModeNormal,
			keys: []string{"2", "w"}, wantBuf: "a b c", wantCur: 4, wantMode: ModeNormal},
		{name: "b to prev word", startBuf: "foo bar", startCur: 4, startMode: ModeNormal,
			keys: []string{"b"}, wantBuf: "foo bar", wantCur: 0, wantMode: ModeNormal},
		{name: "e to word end", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"e"}, wantBuf: "foo bar", wantCur: 2, wantMode: ModeNormal},
		{name: "w across punctuation", startBuf: "a.b", startCur: 0, startMode: ModeNormal,
			keys: []string{"w"}, wantBuf: "a.b", wantCur: 1, wantMode: ModeNormal},
		// vertical
		{name: "j moves down preserving column", startBuf: "abc\ndef", startCur: 1, startMode: ModeNormal,
			keys: []string{"j"}, wantBuf: "abc\ndef", wantCur: 5, wantMode: ModeNormal},
		{name: "k moves up preserving column", startBuf: "abc\ndef", startCur: 5, startMode: ModeNormal,
			keys: []string{"k"}, wantBuf: "abc\ndef", wantCur: 1, wantMode: ModeNormal},
		{name: "j on last line stays", startBuf: "abc\ndef", startCur: 5, startMode: ModeNormal,
			keys: []string{"j"}, wantBuf: "abc\ndef", wantCur: 5, wantMode: ModeNormal},
		{name: "gg to top", startBuf: "abc\ndef", startCur: 5, startMode: ModeNormal,
			keys: []string{"g", "g"}, wantBuf: "abc\ndef", wantCur: 0, wantMode: ModeNormal},
		{name: "G to last line", startBuf: "abc\ndef", startCur: 0, startMode: ModeNormal,
			keys: []string{"G"}, wantBuf: "abc\ndef", wantCur: 4, wantMode: ModeNormal},
		{name: "2G to line 2", startBuf: "a\nb\nc", startCur: 0, startMode: ModeNormal,
			keys: []string{"2", "G"}, wantBuf: "a\nb\nc", wantCur: 2, wantMode: ModeNormal},
		// UTF-8 safety
		{name: "l over multibyte rune", startBuf: "café", startCur: 0, startMode: ModeNormal,
			keys: []string{"l", "l", "l"}, wantBuf: "café", wantCur: 3, wantMode: ModeNormal},
		{name: "$ on multibyte lands on last rune start", startBuf: "café", startCur: 0, startMode: ModeNormal,
			keys: []string{"$"}, wantBuf: "café", wantCur: 3, wantMode: ModeNormal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// --- insert-entry transitions ---

func TestInsertEntry(t *testing.T) {
	cases := []tc{
		{name: "i enters insert at cursor", startBuf: "hi", startCur: 1, startMode: ModeNormal,
			keys: []string{"i"}, wantBuf: "hi", wantCur: 1, wantMode: ModeInsert},
		{name: "a enters insert after cursor", startBuf: "hi", startCur: 0, startMode: ModeNormal,
			keys: []string{"a"}, wantBuf: "hi", wantCur: 1, wantMode: ModeInsert},
		{name: "I enters insert at line start", startBuf: "  hi", startCur: 3, startMode: ModeNormal,
			keys: []string{"I"}, wantBuf: "  hi", wantCur: 0, wantMode: ModeInsert},
		{name: "A enters insert at line end", startBuf: "hi", startCur: 0, startMode: ModeNormal,
			keys: []string{"A"}, wantBuf: "hi", wantCur: 2, wantMode: ModeInsert},
		{name: "o opens line below", startBuf: "ab\ncd", startCur: 0, startMode: ModeNormal,
			keys: []string{"o"}, wantBuf: "ab\n\ncd", wantCur: 3, wantMode: ModeInsert},
		{name: "O opens line above", startBuf: "ab\ncd", startCur: 4, startMode: ModeNormal,
			keys: []string{"O"}, wantBuf: "ab\n\ncd", wantCur: 3, wantMode: ModeInsert},
		{name: "O on first line", startBuf: "ab", startCur: 1, startMode: ModeNormal,
			keys: []string{"O"}, wantBuf: "\nab", wantCur: 0, wantMode: ModeInsert},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// --- one-shot edits: x D C s r ---

func TestOneShotEdits(t *testing.T) {
	cases := []tc{
		{name: "x deletes char under cursor", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"x"}, wantBuf: "ello", wantCur: 0, wantMode: ModeNormal, wantReg: "h", wantLine: ptrBool(false)},
		{name: "x at end of line snaps cursor back", startBuf: "hi", startCur: 1, startMode: ModeNormal,
			keys: []string{"x"}, wantBuf: "h", wantCur: 0, wantMode: ModeNormal, wantReg: "i"},
		{name: "3x deletes three", startBuf: "abcdef", startCur: 0, startMode: ModeNormal,
			keys: []string{"3", "x"}, wantBuf: "def", wantCur: 0, wantMode: ModeNormal, wantReg: "abc"},
		{name: "x does not cross newline", startBuf: "ab\ncd", startCur: 1, startMode: ModeNormal,
			keys: []string{"3", "x"}, wantBuf: "a\ncd", wantCur: 0, wantMode: ModeNormal, wantReg: "b"},
		{name: "D deletes to end of line", startBuf: "hello world", startCur: 6, startMode: ModeNormal,
			keys: []string{"D"}, wantBuf: "hello ", wantCur: 5, wantMode: ModeNormal, wantReg: "world"},
		{name: "C changes to end of line", startBuf: "hello world", startCur: 6, startMode: ModeNormal,
			keys: []string{"C"}, wantBuf: "hello ", wantCur: 6, wantMode: ModeInsert, wantReg: "world"},
		{name: "s substitutes char", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"s"}, wantBuf: "ello", wantCur: 0, wantMode: ModeInsert, wantReg: "h"},
		{name: "r replaces char", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"r", "j"}, wantBuf: "jello", wantCur: 0, wantMode: ModeNormal},
		{name: "r with multibyte replacement", startBuf: "hello", startCur: 1, startMode: ModeNormal,
			keys: []string{"r", "é"}, wantBuf: "héllo", wantCur: 1, wantMode: ModeNormal},
		{name: "r esc cancels", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"r", "esc"}, wantBuf: "hello", wantCur: 0, wantMode: ModeNormal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// --- line-wise dd / cc / yy ---

func TestLinewiseDoubled(t *testing.T) {
	cases := []tc{
		{name: "dd deletes line", startBuf: "one\ntwo\nthree", startCur: 4, startMode: ModeNormal,
			keys: []string{"d", "d"}, wantBuf: "one\nthree", wantCur: 4, wantMode: ModeNormal,
			wantReg: "two\n", wantLine: ptrBool(true)},
		{name: "dd last line", startBuf: "one\ntwo", startCur: 4, startMode: ModeNormal,
			keys: []string{"d", "d"}, wantBuf: "one\n", wantCur: 4, wantMode: ModeNormal,
			wantReg: "two", wantLine: ptrBool(true)},
		{name: "2dd deletes two lines", startBuf: "a\nb\nc\nd", startCur: 0, startMode: ModeNormal,
			keys: []string{"2", "d", "d"}, wantBuf: "c\nd", wantCur: 0, wantMode: ModeNormal,
			wantReg: "a\nb\n", wantLine: ptrBool(true)},
		{name: "yy yanks line (buffer unchanged)", startBuf: "one\ntwo", startCur: 0, startMode: ModeNormal,
			keys: []string{"y", "y"}, wantBuf: "one\ntwo", wantCur: 0, wantMode: ModeNormal,
			wantReg: "one\n", wantLine: ptrBool(true)},
		{name: "cc clears line content, enters insert", startBuf: "one\ntwo", startCur: 1, startMode: ModeNormal,
			keys: []string{"c", "c"}, wantBuf: "\ntwo", wantCur: 0, wantMode: ModeInsert,
			wantReg: "one\n", wantLine: ptrBool(true)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// --- operator + motion combos (counts) ---

func TestOperatorMotion(t *testing.T) {
	cases := []tc{
		{name: "dw deletes word", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"d", "w"}, wantBuf: "bar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo "},
		{name: "de deletes to word end inclusive", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"d", "e"}, wantBuf: " bar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo"},
		{name: "db deletes back", startBuf: "foo bar", startCur: 4, startMode: ModeNormal,
			keys: []string{"d", "b"}, wantBuf: "bar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo "},
		{name: "d$ deletes to end of line", startBuf: "hello world", startCur: 6, startMode: ModeNormal,
			keys: []string{"d", "$"}, wantBuf: "hello ", wantCur: 5, wantMode: ModeNormal, wantReg: "world"},
		// Multiline: d$ must stop AT the newline, not eat it (Codex P2). Before
		// the fix this yielded "world" (newline consumed).
		{name: "d$ on multiline keeps the newline", startBuf: "hello\nworld", startCur: 0, startMode: ModeNormal,
			keys: []string{"d", "$"}, wantBuf: "\nworld", wantCur: 0, wantMode: ModeNormal, wantReg: "hello"},
		{name: "d0 deletes to line start", startBuf: "hello", startCur: 3, startMode: ModeNormal,
			keys: []string{"d", "0"}, wantBuf: "lo", wantCur: 0, wantMode: ModeNormal, wantReg: "hel"},
		{name: "cw changes word", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"c", "w"}, wantBuf: "bar", wantCur: 0, wantMode: ModeInsert, wantReg: "foo "},
		{name: "ce changes to word end", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"c", "e"}, wantBuf: " bar", wantCur: 0, wantMode: ModeInsert, wantReg: "foo"},
		{name: "cb changes back", startBuf: "foo bar", startCur: 4, startMode: ModeNormal,
			keys: []string{"c", "b"}, wantBuf: "bar", wantCur: 0, wantMode: ModeInsert, wantReg: "foo "},
		{name: "c$ changes to end of line", startBuf: "hello world", startCur: 6, startMode: ModeNormal,
			keys: []string{"c", "$"}, wantBuf: "hello ", wantCur: 6, wantMode: ModeInsert, wantReg: "world"},
		{name: "yw yanks word (buffer unchanged)", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"y", "w"}, wantBuf: "foo bar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo "},
		{name: "ye yanks to word end", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"y", "e"}, wantBuf: "foo bar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo"},
		{name: "yb yanks back", startBuf: "foo bar", startCur: 4, startMode: ModeNormal,
			keys: []string{"y", "b"}, wantBuf: "foo bar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo "},
		{name: "y$ yanks to end of line", startBuf: "hello world", startCur: 6, startMode: ModeNormal,
			keys: []string{"y", "$"}, wantBuf: "hello world", wantCur: 6, wantMode: ModeNormal, wantReg: "world"},
		{name: "2dw deletes two words", startBuf: "a b c d", startCur: 0, startMode: ModeNormal,
			keys: []string{"2", "d", "w"}, wantBuf: "c d", wantCur: 0, wantMode: ModeNormal, wantReg: "a b "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// --- paste: p / P, char-wise + line-wise ---

func TestPaste(t *testing.T) {
	// char-wise: yank a word then paste it.
	t.Run("p after char-wise yank", func(t *testing.T) {
		e := New()
		e.mode = ModeNormal
		buf, cur := "ab", 0
		// x deletes 'a' into register (char-wise)
		r := e.Handle("x", buf, cur)
		buf, cur = r.NewBuf, r.NewCursor // buf="b", cur=0
		// p pastes 'a' after cursor → "ba"
		r = e.Handle("p", buf, cur)
		if r.NewBuf != "ba" {
			t.Errorf("p: buf=%q want %q", r.NewBuf, "ba")
		}
		if r.NewCursor != 1 {
			t.Errorf("p: cursor=%d want 1", r.NewCursor)
		}
	})
	t.Run("P before char-wise", func(t *testing.T) {
		e := New()
		e.mode = ModeNormal
		buf, cur := "ab", 1
		r := e.Handle("x", buf, cur) // delete 'b' → "a", reg="b"
		buf, cur = r.NewBuf, r.NewCursor
		r = e.Handle("P", buf, cur) // paste 'b' before cursor
		if r.NewBuf != "ba" {
			t.Errorf("P: buf=%q want %q", r.NewBuf, "ba")
		}
	})
	t.Run("p line-wise after dd", func(t *testing.T) {
		e := New()
		e.mode = ModeNormal
		buf, cur := "one\ntwo\nthree", 0
		r := e.Handle("d", buf, cur)
		buf, cur = r.NewBuf, r.NewCursor
		r = e.Handle("d", buf, cur) // dd line 1 → buf="two\nthree", reg="one\n"
		buf, cur = r.NewBuf, r.NewCursor
		r = e.Handle("p", buf, cur) // paste below line 1 (two)
		want := "two\none\nthree"
		if r.NewBuf != want {
			t.Errorf("p linewise: buf=%q want %q", r.NewBuf, want)
		}
	})
	t.Run("P line-wise before", func(t *testing.T) {
		e := New()
		e.mode = ModeNormal
		buf, cur := "one\ntwo", 4 // cursor on line 2
		r := e.Handle("y", buf, cur)
		buf, cur = r.NewBuf, r.NewCursor
		r = e.Handle("y", buf, cur) // yy line 2 → reg="two", linewise
		buf, cur = r.NewBuf, r.NewCursor
		r = e.Handle("P", buf, cur) // paste above line 2
		want := "one\ntwo\ntwo"
		if r.NewBuf != want {
			t.Errorf("P linewise: buf=%q want %q", r.NewBuf, want)
		}
	})
}

// --- visual mode ---

func TestVisual(t *testing.T) {
	cases := []tc{
		{name: "v enters visual", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"v"}, wantBuf: "hello", wantCur: 0, wantMode: ModeVisual},
		{name: "v l d deletes selection (2 runes inclusive)", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"v", "l", "d"}, wantBuf: "llo", wantCur: 0, wantMode: ModeNormal, wantReg: "he"},
		{name: "v l x deletes selection", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"v", "l", "x"}, wantBuf: "llo", wantCur: 0, wantMode: ModeNormal, wantReg: "he"},
		{name: "v $ y yanks to end inclusive", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"v", "$", "y"}, wantBuf: "hello", wantCur: 0, wantMode: ModeNormal, wantReg: "hello"},
		{name: "v l c changes selection enters insert", startBuf: "hello", startCur: 0, startMode: ModeNormal,
			keys: []string{"v", "l", "c"}, wantBuf: "llo", wantCur: 0, wantMode: ModeInsert, wantReg: "he"},
		{name: "v esc returns to normal", startBuf: "hello", startCur: 2, startMode: ModeNormal,
			keys: []string{"v", "esc"}, wantBuf: "hello", wantCur: 2, wantMode: ModeNormal},
		// Visual char-wise selection is inclusive of the rune under the live
		// cursor: v at 0, w lands on the 'b' (index 4), d deletes [0,4]
		// inclusive → "foo b", leaving "ar". Matches real vim's vwd.
		{name: "v w d deletes inclusive of next-word head", startBuf: "foo bar", startCur: 0, startMode: ModeNormal,
			keys: []string{"v", "w", "d"}, wantBuf: "ar", wantCur: 0, wantMode: ModeNormal, wantReg: "foo b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// --- mode transitions / ESC semantics ---

func TestModeTransitions(t *testing.T) {
	t.Run("engine starts in insert", func(t *testing.T) {
		e := New()
		if e.Mode() != ModeInsert {
			t.Fatalf("New() mode = %v, want INSERT", e.Mode())
		}
	})
	t.Run("insert-mode keys are not consumed", func(t *testing.T) {
		e := New() // INSERT
		r := e.Handle("h", "abc", 0)
		if r.Consumed {
			t.Errorf("INSERT consumed key %q, want pass-through", "h")
		}
		if r.NewBuf != "abc" || r.NewCursor != 0 {
			t.Errorf("INSERT mutated buffer: %q@%d", r.NewBuf, r.NewCursor)
		}
	})
	t.Run("SetMode normal then i back to insert", func(t *testing.T) {
		e := New()
		e.SetMode(ModeNormal)
		r := e.Handle("i", "abc", 1)
		if !r.Consumed {
			t.Errorf("i in NORMAL should be consumed")
		}
		if e.Mode() != ModeInsert {
			t.Errorf("after i, mode = %v, want INSERT", e.Mode())
		}
	})
	t.Run("normal-mode unknown key is consumed (no leak to editor)", func(t *testing.T) {
		e := New()
		e.SetMode(ModeNormal)
		r := e.Handle("z", "abc", 0)
		if !r.Consumed {
			t.Errorf("NORMAL unknown key %q not consumed — would leak to editor", "z")
		}
		if r.NewBuf != "abc" {
			t.Errorf("NORMAL unknown key mutated buffer: %q", r.NewBuf)
		}
	})
	t.Run("count cleared after motion", func(t *testing.T) {
		e := New()
		e.SetMode(ModeNormal)
		buf := "abcdef"
		r := e.Handle("3", buf, 0)
		r = e.Handle("l", buf, r.NewCursor) // 3l → col 3
		if r.NewCursor != 3 {
			t.Fatalf("3l cursor = %d want 3", r.NewCursor)
		}
		// Next l should move only one (count was cleared).
		r = e.Handle("l", buf, r.NewCursor)
		if r.NewCursor != 4 {
			t.Errorf("subsequent l cursor = %d want 4 (count leaked?)", r.NewCursor)
		}
	})
}
