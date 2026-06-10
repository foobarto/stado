package ptyblock

import (
	tea "charm.land/bubbletea/v2"
)

// keyMsgToBytes translates a bubbletea KeyMsg into the byte sequence
// a real terminal would deliver to the spawned process. The block's
// "shell-input mode" (Day 3) routes keystrokes through this translator
// and then through Writer.Write to feed the PTY's stdin.
//
// What's covered:
//
//   - Plain runes (a, b, …) → UTF-8 byte form.
//   - Enter / Tab / Backspace / Esc → their canonical control bytes.
//   - Arrow keys / Home / End / PageUp / PageDown → ANSI CSI sequences
//     (`\x1b[A`, `\x1b[H`, etc.) — the same form xterm emits, which
//     is what readline / vim / curses-based programs expect.
//   - F1–F12 → xterm function-key sequences.
//   - Ctrl+letter → its 0x01–0x1A control byte (Ctrl+A = 0x01,
//     Ctrl+C = 0x03, etc.). Ctrl+@ → NUL.
//
// Notably NOT translated here:
//
//   - Alt+key → would be `\x1b<key>` (ESC prefix). Skipped for v1
//     because bubbletea's Alt detection is platform-quirky and
//     supporting it cleanly needs a Mod field check the embedding
//     TUI doesn't yet wire. Most TUI programs operators care about
//     (vim, htop, less) work without Alt.
//
// Reserved (NOT translated, but for a different reason):
//
//   - Ctrl+] → the leave-mode gesture. Model.HandleKey intercepts
//     it before reaching this translator and returns handled=false
//     so the parent TUI sees it. Operators hit Ctrl+] to leave
//     shell-input mode; everything else (Esc, Tab, SHIFT-TAB,
//     function keys, control bytes) reaches the PTY.
//
// History note: a previous version of the model reserved Esc / Tab /
// SHIFT-TAB instead, which broke vim (Esc = exit insert mode), shell
// autocomplete (Tab), and editor normal-mode navigation (SHIFT-TAB →
// \x1b[Z). The 2026-05-09 second-pass review caught that and the
// reserved set switched to Ctrl+] only.
//
// Returns (bytes, true) when the key is recognised. Unrecognised keys
// return (nil, false) so the caller can decide whether to swallow or
// surface a "key not forwarded" hint.
func keyMsgToBytes(msg tea.KeyPressMsg) ([]byte, bool) {
	// Special / control keys first — the Code discriminator covers most
	// of the named keys without having to inspect the typed text.
	switch msg.Code {
	case tea.KeyEnter:
		// CR is the historical convention for terminal Enter; programs
		// expecting LF run inside their own line discipline anyway.
		return []byte{'\r'}, true
	case tea.KeyTab:
		// Should never be reached — TAB is the enter-mode gesture.
		// Emit the Tab byte anyway in case the embedding TUI decides
		// to forward it explicitly; defensive.
		return []byte{'\t'}, true
	case tea.KeyBackspace:
		// 0x7F (DEL) is what xterm sends for Backspace by default —
		// readline's BackwardDeleteChar binding hits it. 0x08 (BS)
		// also works for many programs but is less universal.
		return []byte{0x7F}, true
	case tea.KeyEsc:
		return []byte{0x1B}, true
	case tea.KeyDelete:
		return []byte("\x1b[3~"), true
	case tea.KeyHome:
		return []byte("\x1b[H"), true
	case tea.KeyEnd:
		return []byte("\x1b[F"), true
	case tea.KeyPgUp:
		return []byte("\x1b[5~"), true
	case tea.KeyPgDown:
		return []byte("\x1b[6~"), true
	case tea.KeyUp:
		return []byte("\x1b[A"), true
	case tea.KeyDown:
		return []byte("\x1b[B"), true
	case tea.KeyRight:
		return []byte("\x1b[C"), true
	case tea.KeyLeft:
		return []byte("\x1b[D"), true
	case tea.KeyF1:
		return []byte("\x1bOP"), true
	case tea.KeyF2:
		return []byte("\x1bOQ"), true
	case tea.KeyF3:
		return []byte("\x1bOR"), true
	case tea.KeyF4:
		return []byte("\x1bOS"), true
	case tea.KeyF5:
		return []byte("\x1b[15~"), true
	case tea.KeyF6:
		return []byte("\x1b[17~"), true
	case tea.KeyF7:
		return []byte("\x1b[18~"), true
	case tea.KeyF8:
		return []byte("\x1b[19~"), true
	case tea.KeyF9:
		return []byte("\x1b[20~"), true
	case tea.KeyF10:
		return []byte("\x1b[21~"), true
	case tea.KeyF11:
		return []byte("\x1b[23~"), true
	case tea.KeyF12:
		return []byte("\x1b[24~"), true
	case tea.KeySpace:
		// Some terminals route space via the dedicated space Code
		// rather than carrying it only in Text; emit the space byte.
		return []byte{' '}, true
	}

	// Ctrl+letter family. In v1 these arrived as the contiguous
	// tea.KeyCtrlA…tea.KeyCtrlUnderscore KeyTypes (0x01–0x1F) and the
	// byte the program expects equals that numeric value. In v2 the same
	// combos arrive as a printable Code (e.g. 'a') with ModCtrl set; the
	// control byte is the low five bits of the code. ctrl+@ / ctrl+space
	// (byte 0x00) was not translated in v1, so it stays excluded here.
	//
	// Restricted to printable ASCII codes: v2's special-key constants
	// (KeyInsert, modified arrows, media keys, …) are runes above
	// unicode.MaxRune, and masking those with 0x1f would fabricate an
	// unrelated control byte — e.g. Ctrl+Insert sending Ctrl+C/EOF-class
	// bytes that interrupt or terminate the PTY program.
	if msg.Mod.Contains(tea.ModCtrl) && msg.Code >= 0x20 && msg.Code < 0x80 {
		if b := byte(msg.Code & 0x1f); b >= 0x01 && b <= 0x1f {
			return []byte{b}, true
		}
	}

	// Plain rune typing — emit the UTF-8 byte form of the characters
	// received (the v1 KeyRunes path).
	if msg.Text != "" {
		return []byte(msg.Text), true
	}

	return nil, false
}
