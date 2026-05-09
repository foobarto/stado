package ptyblock

import (
	tea "github.com/charmbracelet/bubbletea"
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
//   - The TAB / SHIFT-TAB keys themselves — those are intercepted by
//     the parent TUI's input router as the enter / leave gestures
//     for shell-input mode. They never reach the PTY.
//
// Returns (bytes, true) when the key is recognised. Unrecognised keys
// return (nil, false) so the caller can decide whether to swallow or
// surface a "key not forwarded" hint.
func keyMsgToBytes(msg tea.KeyMsg) ([]byte, bool) {
	// Special / control keys first — KeyType discriminator covers most
	// of the named keys without having to inspect runes.
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		// Plain rune typing or the space key. Runes carry the actual
		// characters typed; emit their UTF-8 form. Spaces from
		// KeySpace are special-cased because some terminals route
		// space as KeySpace rather than KeyRunes.
		if msg.Type == tea.KeySpace && len(msg.Runes) == 0 {
			return []byte{' '}, true
		}
		return []byte(string(msg.Runes)), true
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
	}

	// Ctrl+letter family. bubbletea encodes Ctrl+A as KeyCtrlA (= 0x01)
	// directly in the KeyType numeric range; the byte the program
	// expects is the same numeric value. tea.KeyCtrlA …
	// tea.KeyCtrlUnderscore are contiguous from 0x01 to 0x1F.
	if msg.Type >= tea.KeyCtrlA && msg.Type <= tea.KeyCtrlUnderscore {
		return []byte{byte(msg.Type)}, true
	}

	return nil, false
}
