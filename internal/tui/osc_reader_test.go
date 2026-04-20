package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// readAll drains the stripping reader into a string so tests can
// assert against the resulting byte stream.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 128)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

// TestOSCStripReader_BELTerminator: an OSC 11 response ending in BEL
// (the short xterm form) is fully elided. Plain bytes before/after
// are preserved.
func TestOSCStripReader_BELTerminator(t *testing.T) {
	src := "hello\x1b]11;rgb:1e1e/1e1e/1e1e\x07world"
	got := readAll(t, newOSCStripReader(strings.NewReader(src)))
	if got != "helloworld" {
		t.Errorf("got %q, want %q", got, "helloworld")
	}
}

// TestOSCStripReader_STTerminator: ST form (\x1b\\) also elides
// cleanly.
func TestOSCStripReader_STTerminator(t *testing.T) {
	src := "abc\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\xyz"
	got := readAll(t, newOSCStripReader(strings.NewReader(src)))
	if got != "abcxyz" {
		t.Errorf("got %q, want %q", got, "abcxyz")
	}
}

// TestOSCStripReader_SplitAcrossReads: the terminator may land in a
// different Read call than the body. State must persist across
// Read-boundaries, else the tail leaks. This is exactly the bug the
// tea.WithFilter approach missed.
func TestOSCStripReader_SplitAcrossReads(t *testing.T) {
	// Arrange: emit the sequence in two chunks, with a plain byte
	// after the terminator.
	first := "abc\x1b]11;rgb:1e1e/1e1e/"
	second := "1e1e\x1b\\xyz"
	r := newOSCStripReader(&sequentialReader{chunks: []string{first, second}})
	got := readAll(t, r)
	if got != "abcxyz" {
		t.Errorf("got %q, want %q", got, "abcxyz")
	}
}

// TestOSCStripReader_LoneESCFallsThrough: an ESC that's NOT followed
// by ']' must re-emit as-is — otherwise the reader would swallow
// legitimate Alt-prefixed keybinds or CSI sequences.
func TestOSCStripReader_LoneESCFallsThrough(t *testing.T) {
	// \x1b[A is a CSI Up-arrow; must survive the reader unchanged.
	src := "\x1b[A"
	got := readAll(t, newOSCStripReader(strings.NewReader(src)))
	if got != "\x1b[A" {
		t.Errorf("got %q, want %q", got, "\x1b[A")
	}
	// Alt+x (ESC x) also survives.
	src2 := "\x1bx"
	got2 := readAll(t, newOSCStripReader(strings.NewReader(src2)))
	if got2 != "\x1bx" {
		t.Errorf("got %q, want %q", got2, "\x1bx")
	}
}

// TestOSCStripReader_MultipleSequences: two OSC responses back to
// back (which is what the user saw in the textarea screenshot) both
// get elided.
func TestOSCStripReader_MultipleSequences(t *testing.T) {
	src := "A\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\B\x1b]10;rgb:ffff/ffff/ffff\x07C"
	got := readAll(t, newOSCStripReader(strings.NewReader(src)))
	if got != "ABC" {
		t.Errorf("got %q, want %q", got, "ABC")
	}
}

// TestOSCStripReader_PlainPassthrough: no OSC in the stream → bytes
// unchanged.
func TestOSCStripReader_PlainPassthrough(t *testing.T) {
	src := "the quick brown fox"
	got := readAll(t, newOSCStripReader(strings.NewReader(src)))
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

// TestOSCStripReader_LoneESCFlushes: a single ESC byte arriving
// alone (user pressing Escape) must propagate to the caller. The
// first Read into the reader returns the ESC; holding it would
// swallow the Escape key (the reported TUI-broken bug).
func TestOSCStripReader_LoneESCFlushes(t *testing.T) {
	// Emit the ESC alone in its own Read chunk to simulate a real
	// keyboard Escape press.
	r := newOSCStripReader(&sequentialReader{chunks: []string{"\x1b"}})
	buf := make([]byte, 4)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 1 || buf[0] != 0x1b {
		t.Errorf("lone ESC should flush; got n=%d buf=% x", n, buf[:n])
	}
}

// TestOSCStripReader_ESCThenTypingInSameReadPreserved: ESC followed
// by non-bracket in the SAME Read flushes both (lone Esc followed
// by a keypress). Also covered by TestOSCStripReader_LoneESCFalls
// Through — this one specifically pins the "alt+key" shape that
// bubbletea treats as a modified keystroke.
func TestOSCStripReader_ESCThenTypingInSameReadPreserved(t *testing.T) {
	src := "\x1bx"
	got := readAll(t, newOSCStripReader(strings.NewReader(src)))
	if got != "\x1bx" {
		t.Errorf("alt+x sequence lost; got %q", got)
	}
}

// TestOSCStripFile_ExposesFdNameWriteClose verifies the production
// wrapper keeps the *os.File's term.File + cancelreader.File surface
// intact. Without these, bubbletea silently falls back to cooked-mode
// stdin and keyboard input breaks completely (keystrokes echo to the
// terminal cursor; nothing reaches the TUI).
func TestOSCStripFile_ExposesFdNameWriteClose(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	sf := newOSCStripFile(pr)

	// Fd must forward to the underlying file — this is what bubbletea
	// type-asserts on to decide whether to enter raw mode.
	if sf.Fd() != pr.Fd() {
		t.Errorf("Fd mismatch: got %d, want %d", sf.Fd(), pr.Fd())
	}
	if sf.Name() != pr.Name() {
		t.Errorf("Name mismatch: got %q, want %q", sf.Name(), pr.Name())
	}

	// Read filters: write an OSC sequence to the pipe, read through
	// the wrapper, confirm it's elided.
	go func() {
		_, _ = pw.WriteString("A\x1b]11;rgb:1/1/1\x07Z")
		_ = pw.Close()
	}()
	got := readAll(t, sf)
	if got != "AZ" {
		t.Errorf("filtered read got %q, want %q", got, "AZ")
	}
}

// sequentialReader returns chunks one Read() call at a time so tests
// can simulate input arriving in separate OS buffer fills — exactly
// how a terminal's late OSC response is likely to appear after the
// user has already started typing.
type sequentialReader struct {
	chunks []string
	i      int
}

func (r *sequentialReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.i]
	r.i++
	n := copy(p, chunk)
	return n, nil
}
