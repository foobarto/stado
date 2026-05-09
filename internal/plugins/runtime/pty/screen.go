// Screen captures a frozen view of a session's terminal emulator —
// the rendered grid as a downstream consumer would see if a real
// terminal were drawing it. The cell grid is what the snapshot tool
// turns into text or SVG; nothing else in the package consumes it.
//
// Attribute bits are kept in lockstep with vt10x's package-private
// constants (state.go in vt10x v0.0.0-20220301184237 — at the time
// of writing, hinshun/vt10x has not bumped the values since 2017).
// vt10x exposes Glyph.Mode as int16 but does not export the bit
// names, so we redefine them here. If vt10x ever shifts the bits
// (semver-major-shaped change for an unmaintained package — unlikely)
// the SVG output would lose attribute styling but text would still
// be correct, and the snapshot test would catch it.
package pty

import "github.com/hinshun/vt10x"

// Snapshot freezes the emulator state for the named session. Returns
// ErrNotFound if the session has been destroyed. Returns the empty
// screen for a session that closed before producing any output.
//
// No attach required: snapshots are read-only and cheap (allocate a
// rows×cols cell grid, copy glyphs out, release the lock). Operators
// pulling a snapshot during another caller's read race-free; vt10x's
// internal state is read while we hold session.mu, which the drain
// goroutine also takes before writing.
//
// The Version field on the returned Screen lets polling consumers
// (TUI ptyblock, etc.) skip work when nothing's changed: cache the
// last-seen Version, then call SnapshotVersion before re-allocating
// a full grid. See SnapshotIfChanged for the convenience wrapper.
func (m *Manager) Snapshot(id uint64) (*Screen, error) {
	s, err := m.get(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touch()
	return snapshotLocked(s), nil
}

// SnapshotVersion returns the current per-session change counter
// without copying the cell grid. Counter increments on every drain
// chunk (see session.version). Cheap — just a lock + uint64 read.
//
// Caller pattern:
//
//	v, err := mgr.SnapshotVersion(id)
//	if err != nil { return err }
//	if v == lastSeen { return /* no change */ }
//	scr, err := mgr.Snapshot(id) // will return Version v
//	lastSeen = scr.Version
//
// Or use SnapshotIfChanged for the same shape in one call.
//
// Note: SnapshotVersion does NOT touch lastClientTouch — it's a
// read-only peek and shouldn't keep an otherwise-idle session alive
// against the watchdog. Snapshot does touch (active client interest).
func (m *Manager) SnapshotVersion(id uint64) (uint64, error) {
	s, err := m.get(id)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	v := s.version
	s.mu.Unlock()
	return v, nil
}

// SnapshotIfChanged returns the current screen iff its Version
// differs from sinceVersion. When unchanged, returns (nil, sinceVersion,
// nil) — the caller's existing frame is still authoritative. When
// changed (or sinceVersion is 0 / not yet known), allocates and
// returns the fresh screen.
//
// Always touches lastClientTouch (the caller is actively polling),
// regardless of whether a snapshot was produced. Without this,
// version-poll consumers would silently fall behind the watchdog.
func (m *Manager) SnapshotIfChanged(id, sinceVersion uint64) (*Screen, uint64, error) {
	s, err := m.get(id)
	if err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touch()
	if s.version == sinceVersion {
		return nil, sinceVersion, nil
	}
	return snapshotLocked(s), s.version, nil
}

// snapshotLocked builds a Screen from the session's emulator state.
// Caller must hold s.mu. Shared between Snapshot and
// SnapshotIfChanged so both paths produce identical output.
func snapshotLocked(s *session) *Screen {
	cols := int(s.cols)
	rows := int(s.rows)
	cells := make([][]Cell, rows)
	for y := 0; y < rows; y++ {
		row := make([]Cell, cols)
		for x := 0; x < cols; x++ {
			g := s.vt.Cell(x, y)
			row[x] = Cell{
				Char:  g.Char,
				FG:    uint32(g.FG),
				BG:    uint32(g.BG),
				Attrs: uint16(g.Mode),
			}
		}
		cells[y] = row
	}
	cur := s.vt.Cursor()
	return &Screen{
		Cols:    cols,
		Rows:    rows,
		Cells:   cells,
		Cursor:  CursorPos{X: cur.X, Y: cur.Y, Visible: s.vt.CursorVisible()},
		Title:   s.vt.Title(),
		Version: s.version,
	}
}

// Screen is a render-time-frozen view of the emulator. Cells is
// indexed [row][col]; both axes are 0-based. Cells outside the grid
// (e.g. when a row is short of cols) are not represented — every row
// is exactly Cols wide.
//
// Version is the per-session change counter at the time of snapshot.
// Polling consumers cache the last-seen Version and skip the next
// full snapshot when SnapshotVersion (or SnapshotIfChanged) reports
// no change. Bumps once per drain chunk; never wraps in practice.
type Screen struct {
	Cols, Rows int
	Cells      [][]Cell
	Cursor     CursorPos
	Title      string
	Version    uint64
}

// Cell is one grid position. Char==0 means the glyph slot is empty
// (no character drawn); SVG/text rendering treats those as spaces.
// FG/BG are vt10x's Color values, with the encoding documented in
// vt10x/color.go: ANSI palette indices 0–15 fit in the low byte and
// satisfy `Color.ANSI()`; truecolor is 0xRRGGBB | 0x01000000 (any
// value ≥ 16 with the alpha bit set). The renderer maps both forms
// to RGB strings.
type Cell struct {
	Char  rune
	FG    uint32
	BG    uint32
	Attrs uint16
}

// CursorPos is the cursor's grid position plus its display visibility
// state (DECTCEM). Visible=false maps to "do not render the cursor"
// in SVG output.
type CursorPos struct {
	X, Y    int
	Visible bool
}

// Attribute bits — mirrored from vt10x state.go's package-private
// constants. See file-level doc comment for the rationale.
const (
	AttrReverse   uint16 = 1 << 0
	AttrUnderline uint16 = 1 << 1
	AttrBold      uint16 = 1 << 2
	AttrGfx       uint16 = 1 << 3
	AttrItalic    uint16 = 1 << 4
	AttrBlink     uint16 = 1 << 5
)

// ColorRGB resolves a vt10x cell color to an 0xRRGGBB triplet. The
// 16 ANSI colors map to ansiPalette; values flagged truecolor by
// vt10x.Color.ANSI()==false pass through unchanged (low 24 bits).
//
// vt10x stores ANSI colors as small ints 0–15 and truecolor as
// 0x01RRGGBB (the high byte is a "this is truecolor" tag). Either way,
// masking the low 24 bits is the right resolution rule.
func ColorRGB(c uint32) uint32 {
	if vt10x.Color(c).ANSI() {
		return ansiPalette[c&0x0F]
	}
	return c & 0xFFFFFF
}

// ansiPalette is the 16-color ANSI palette used when a cell carries
// an indexed color. The values match `hack/pty-bridge`'s xterm.js
// theme so a snapshot rendered as SVG looks like what an operator
// would see in a live xterm-256color session. Order matches the
// SGR/ECMA-48 sequence: black, red, green, yellow, blue, magenta,
// cyan, white, then the 8 bright variants.
var ansiPalette = [16]uint32{
	0x000000, // 0 black
	0xCD3131, // 1 red
	0x0DBC79, // 2 green
	0xE5E510, // 3 yellow
	0x2472C8, // 4 blue
	0xBC3FBC, // 5 magenta
	0x11A8CD, // 6 cyan
	0xE5E5E5, // 7 white
	0x666666, // 8 bright black (gray)
	0xF14C4C, // 9 bright red
	0x23D18B, // 10 bright green
	0xF5F543, // 11 bright yellow
	0x3B8EEA, // 12 bright blue
	0xD670D6, // 13 bright magenta
	0x29B8DB, // 14 bright cyan
	0xFFFFFF, // 15 bright white
}

// IsDefaultBG returns true for the cell's BG when it should not be
// drawn explicitly (the page-level background covers it). vt10x
// initialises every cell to FG=7, BG=0; the SVG renderer treats BG=0
// as the canvas color so unwritten regions don't get redundant rects.
func (c Cell) IsDefaultBG() bool { return c.BG == 0 }

// IsDefaultFG returns true for the cell's FG when it equals the
// default-text color. Used by the SVG renderer to skip an explicit
// fill on cells that already match the page-level text color.
func (c Cell) IsDefaultFG() bool { return c.FG == 7 }
