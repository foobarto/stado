package pty

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestSnapshotPlainText: write a known string into a `cat` session and
// confirm the snapshot's text payload contains it. Validates the
// minimum claim: vt10x is being fed bytes from drain, and Snapshot
// reflects emulator state.
func TestSnapshotPlainText(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := m.Write(id, []byte("hello world\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Drain so vt10x has had a chance to consume the echo.
	_ = readUntil(t, m, id, []byte("hello world"), 2*time.Second)
	// Give the drain goroutine a tick to update vt10x state. The ring
	// already has the bytes; the emulator runs in the same critical
	// section so this is normally instant, but on overloaded CI a
	// scheduler delay can race.
	time.Sleep(20 * time.Millisecond)

	snap, err := m.Snapshot(id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Errorf("Snapshot dims = %dx%d, want 80x24", snap.Cols, snap.Rows)
	}
	if !strings.Contains(snap.Text(), "hello world") {
		t.Errorf("Snapshot.Text missing 'hello world':\n%s", snap.Text())
	}
}

// TestSnapshotResize: Resize must update the emulator's grid so a
// follow-up Snapshot reports the new dims. Without the lockstep call
// in Resize, vt10x would stay at the original size while creack/pty
// reports the new one — the kind of drift that becomes a load-bearing
// bug under autotest.
func TestSnapshotResize(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Resize(id, 120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	snap, err := m.Snapshot(id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Cols != 120 || snap.Rows != 40 {
		t.Errorf("after Resize, snap dims = %dx%d, want 120x40", snap.Cols, snap.Rows)
	}
}

// TestSVGSelfContained: SVG output is a complete document that starts
// with <svg, ends with </svg>, and contains a rect for the canvas
// background. Doesn't render — just guards against malformed output.
// Visual assertions live in fixture tests below.
func TestSVGSelfContained(t *testing.T) {
	scr := &Screen{
		Cols: 5, Rows: 2,
		Cells: [][]Cell{
			{{Char: 'h', FG: 7, BG: 0}, {Char: 'i', FG: 7, BG: 0}, {Char: 0}, {Char: 0}, {Char: 0}},
			{{Char: 0}, {Char: 0}, {Char: 0}, {Char: 0}, {Char: 0}},
		},
		Cursor: CursorPos{X: 2, Y: 0, Visible: true},
	}
	out := scr.SVG(nil)
	if !strings.HasPrefix(out, "<svg") {
		t.Errorf("SVG: want <svg prefix, got %q", out[:min(40, len(out))])
	}
	if !strings.HasSuffix(out, "</svg>") {
		t.Errorf("SVG: want </svg> suffix, got %q", tail(out, 40))
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("SVG: missing rendered text:\n%s", out)
	}
	// Cursor: when Visible, an outlined rect at the cursor's pixel
	// position must be emitted. Outline is `fill="none" stroke=...`.
	if !strings.Contains(out, `fill="none"`) {
		t.Errorf("SVG: missing cursor outline rect:\n%s", out)
	}
}

// TestSVGEscaping: shell prompts and TUI titles routinely contain &,
// <, >. The SVG must escape them or the result is invalid XML.
func TestSVGEscaping(t *testing.T) {
	scr := &Screen{
		Cols: 5, Rows: 1,
		Cells: [][]Cell{{
			{Char: '<', FG: 7, BG: 0}, {Char: '>', FG: 7, BG: 0}, {Char: '&', FG: 7, BG: 0},
			{Char: 0}, {Char: 0},
		}},
	}
	out := scr.SVG(nil)
	for _, raw := range []string{"<text", "</text>"} {
		if !strings.Contains(out, raw) {
			continue // we want these tags to remain
		}
	}
	// The literal angle brackets that came from cells must not appear
	// outside their entity-escaped form: the substring "><" or ">&<"
	// is fine in the SVG framework, but a bare ">>" or "<&" coming
	// from the cell content would be malformed XML. Easiest assertion:
	// each cell character appears in entity form.
	for _, want := range []string{"&lt;", "&gt;", "&amp;"} {
		if !strings.Contains(out, want) {
			t.Errorf("SVG: missing entity %q:\n%s", want, out)
		}
	}
}

// TestSVGSizeReasonable: a fully-default 120×32 screen should produce
// an SVG well under 100 KB. Bigger and we've regressed on the
// background-merging optimization.
func TestSVGSizeReasonable(t *testing.T) {
	cells := make([][]Cell, 32)
	for y := range cells {
		row := make([]Cell, 120)
		for x := range row {
			row[x] = Cell{Char: 0, FG: 7, BG: 0}
		}
		cells[y] = row
	}
	scr := &Screen{Cols: 120, Rows: 32, Cells: cells}
	out := scr.SVG(nil)
	if got := len(out); got > 4096 {
		t.Errorf("empty 120×32 SVG = %d bytes, want < 4 KB", got)
	}
}

// TestSnapshotEmptySession: Spawn → immediate Snapshot before any
// output should still return a valid empty Screen, not panic.
func TestSnapshotEmptySession(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	snap, err := m.Snapshot(id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Cols == 0 || snap.Rows == 0 {
		t.Errorf("empty session snap dims = %dx%d, want non-zero", snap.Cols, snap.Rows)
	}
	// Empty SVG should still be a valid document.
	out := snap.SVG(nil)
	if !strings.HasPrefix(out, "<svg") || !strings.HasSuffix(out, "</svg>") {
		t.Errorf("empty SVG malformed: %q…%q", out[:min(40, len(out))], tail(out, 40))
	}
}

// silence unused-import warning when bytes helper isn't needed.
var _ = bytes.Contains

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
