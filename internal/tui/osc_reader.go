package tui

import (
	"io"
)

// oscStripReader wraps an io.Reader and elides terminal OSC response
// sequences from the byte stream before the consumer sees them. Exists
// because bubbletea v1.3's input parser has no OSC handling — when a
// terminal replies to an OSC query (\x1b]<Ps>;...\x07 or \x1b]<Ps>;
// ...\x1b\\), bubbletea treats the ESC as an Alt-modifier opener and
// stuffs the payload bytes into a KeyMsg that lands in the focused
// widget. Users see `]11;rgb:1e1e/1e1e/1e1e\` show up in the
// textarea.
//
// Higher-level tea.WithFilter caught the first chunk but missed
// across-read-boundary splits (tail arrives as a plain rune burst
// with no ESC prefix). Filtering at the byte level before bubbletea
// even parses keeps the fix behind the widget layer entirely.
//
// State machine for one sequence:
//
//	idle           — copying bytes
//	sawESC         — last byte was \x1b; might be OSC start
//	inOSC          — inside \x1b]... body; dropping bytes
//	inOSCsawESC    — inside OSC, last byte was \x1b; next might be \\
//
// Sequence terminators recognised:
//   - \x07 (BEL)      — tmux / xterm short form
//   - \x1b\\ (ST)     — xterm long form
//
// Not bracketed-paste aware (which uses \x1b[200~ ... \x1b[201~ —
// CSI sequences, not OSC). Also not CSI: CSI escapes like bracketed
// paste are consumed by bubbletea's existing unknownCSIRe path, so
// they don't need byte-level stripping.
type oscStripReader struct {
	src   io.Reader
	state oscState
	// carryover caches bytes from an in-progress sequence that ended
	// across a Read-boundary. Not used today — the state machine is
	// stateless across calls — kept here for future tightening.
	_ []byte
}

type oscState int

const (
	oscIdle oscState = iota
	oscSawESC
	oscInOSC
	oscInOSCSawESC
)

// newOSCStripReader wraps src. Safe to wrap os.Stdin directly; not
// thread-safe (one reader per TUI program).
func newOSCStripReader(src io.Reader) *oscStripReader {
	return &oscStripReader{src: src}
}

// Read copies from src into p, dropping any bytes that are part of an
// OSC sequence. Sequence state persists across Read calls so a split
// sequence (terminal flushing mid-\x1b\\) still gets elided cleanly.
// Returns (n, err) where n is the count of kept bytes actually written
// into p — may be less than what was read from src.
func (r *oscStripReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Allocate a scratch buffer the same size as p. Read raw bytes in,
	// filter into the caller's buffer. In the common "no OSC at all"
	// path this is one extra copy per Read — small price for isolation
	// from bubbletea's parser.
	buf := make([]byte, len(p))
	n, err := r.src.Read(buf)
	if n == 0 {
		return 0, err
	}
	w := 0
	for i := 0; i < n; i++ {
		b := buf[i]
		switch r.state {
		case oscIdle:
			if b == 0x1b {
				r.state = oscSawESC
				continue
			}
			p[w] = b
			w++
		case oscSawESC:
			if b == ']' {
				// Confirmed OSC open — eat the ESC we held and enter body.
				r.state = oscInOSC
				continue
			}
			// False alarm — emit the held ESC + current byte as-is.
			p[w] = 0x1b
			w++
			if w < len(p) {
				p[w] = b
				w++
			}
			r.state = oscIdle
		case oscInOSC:
			if b == 0x07 {
				// BEL terminator. Back to idle; drop the BEL.
				r.state = oscIdle
				continue
			}
			if b == 0x1b {
				r.state = oscInOSCSawESC
				continue
			}
			// Body byte — drop.
		case oscInOSCSawESC:
			if b == '\\' {
				// ST terminator: \x1b\\. Drop the backslash too.
				r.state = oscIdle
				continue
			}
			// Nested ESC without a closing backslash — treat as a new
			// OSC start if followed by ']', else drop the held ESC and
			// stay in body. Either way, stay safe: go back to oscInOSC.
			r.state = oscInOSC
		}
	}
	return w, err
}
