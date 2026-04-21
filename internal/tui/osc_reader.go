package tui

import (
	"io"
	"os"
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
//	sawESC         — last byte was \x1b; might be OSC or CSI start
//	inOSC          — inside \x1b]... body; dropping bytes
//	inOSCsawESC    — inside OSC, last byte was \x1b; next might be \\
//	inCSIReport    — inside \x1b[<digits>...; swallow until the final
//	                 letter if it matches a known-response class
//
// Sequence terminators recognised:
//   - \x07 (BEL)      — tmux / xterm short form (OSC)
//   - \x1b\\ (ST)     — xterm long form (OSC)
//   - R | c | n       — CSI cursor-position / device-attrs / report
//
// The CSI handling is NARROW: it only eats CSI sequences whose body
// starts with a digit or semicolon (terminal responses) AND ends in
// a report-class letter (R/c/n). Arrow keys (\x1b[A..D) and function
// keys (\x1b[N~) have no digit prefix or end in ~, so they flow
// through untouched. Bracketed paste (\x1b[200~ ... \x1b[201~) is
// also safe — its final char is ~, not a report letter.
//
// Why this matters: bubbletea v1.3 has no CSI-response parser, so
// terminal cursor-position reports (emitted in reply to CPR queries
// that some themes/runners issue during streaming) leak as visible
// text like `[45;1R` into the focused textarea.
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
	oscInCSIReport
)

// newOSCStripReader wraps src. Tests pass arbitrary readers here; the
// TUI uses newOSCStripFile so bubbletea can still hit the real fd for
// termios + epoll. Not thread-safe (one reader per TUI program).
func newOSCStripReader(src io.Reader) *oscStripReader {
	return &oscStripReader{src: src}
}

// oscStripFile is the production wrapper around os.Stdin: embeds the
// *os.File (so Fd()/Write()/Close()/Name() are exposed verbatim), but
// overrides Read to run through the OSC-stripping state machine.
//
// Both bubbletea's initInput (needs term.File to call MakeRaw on the
// tty fd) and muesli/cancelreader (needs cancelreader.File to epoll
// on the tty fd) type-assert on methods Fd()+Name()+Write()+Close().
// Wrapping as a plain io.Reader — which is what `newOSCStripReader`
// alone produces — silently fell back to cooked-mode stdin: keystrokes
// echoed to the terminal's cursor position and never reached the TUI.
// This type keeps the raw-mode setup path while routing every Read
// through the filter.
type oscStripFile struct {
	*os.File
	r oscStripReader
}

// newOSCStripFile wraps an *os.File. The embedded *os.File satisfies
// every non-Read method of term.File + cancelreader.File, so bubbletea
// and cancelreader never see a demoted io.Reader.
func newOSCStripFile(f *os.File) *oscStripFile {
	sf := &oscStripFile{File: f}
	sf.r.src = f
	return sf
}

// Read routes through the stripper. cancelreader's epollCancelReader
// calls this method (r.file.Read) after the fd signals readable, so
// the filter sees every byte even though epoll waits on the real fd.
func (f *oscStripFile) Read(p []byte) (int, error) { return f.r.Read(p) }

// retractCSIReport walks w backwards, discarding bytes up through
// the most recent `\x1b[` opener that marked the start of a CSI
// report. Returns the new write cursor. Used when a report-class
// final char (R/c/n) confirms the CSI sequence we'd already begun
// emitting is a terminal response that must be suppressed.
func retractCSIReport(p []byte, w int) int {
	for i := w - 1; i >= 0; i-- {
		if p[i] == '[' && i > 0 && p[i-1] == 0x1b {
			return i - 1
		}
	}
	// Shouldn't happen (we only enter oscInCSIReport after emitting
	// ESC[), but defensive: keep what we had.
	return w
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
			if b == '[' {
				// CSI opener. We don't know yet whether this is a
				// report (eatable) or a keystroke (must pass through).
				// Peek-style: emit ESC+[ and also remember to re-check
				// if next byte is a digit. Simpler: hold ESC and '['
				// for one byte, then decide. To avoid that complexity
				// we emit the pair now and switch to a state that
				// watches for a `;`-ended report. If the sequence
				// turns out to be an arrow key (`\x1b[A`) it's already
				// emitted, which is correct.
				p[w] = 0x1b
				w++
				if w < len(p) {
					p[w] = '['
					w++
				}
				r.state = oscInCSIReport
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
		case oscInCSIReport:
			// We've already emitted `\x1b[` to p. Watch the body:
			// if the final char is R/c/n AND we saw at least one
			// digit or semicolon, RETRACT the emitted ESC+[ pair
			// and drop the report entirely. If the final char is
			// anything else (a keystroke final like A/B/C/D/~/letter),
			// emit and return to idle — the ESC+[ we already wrote
			// was correct.
			switch {
			case b >= '0' && b <= '9', b == ';', b == '?', b == ':':
				// Report-body byte. Still "pending" — emit it; we'll
				// retract the whole thing if the final char confirms
				// it's a report.
				p[w] = b
				w++
			case b == 'R' || b == 'c' || b == 'n':
				// Report final. Walk back through the emitted bytes
				// to drop everything from the ESC[.
				w = retractCSIReport(p, w)
				r.state = oscIdle
			default:
				// Non-report final (arrow, ~, letter, etc.). Emit
				// as-is and return to idle.
				p[w] = b
				w++
				r.state = oscIdle
			}
		}
	}
	// End-of-read flush: if the last byte we processed left us in
	// oscSawESC, that means the ESC arrived alone in this Read. It's
	// almost certainly a real Escape keypress (user pressing Esc),
	// not an OSC opener — terminals deliver OSC responses as one
	// atomic burst. Holding the ESC would swallow the Escape key
	// across the session. Emit it now so bubbletea sees it; the next
	// Read starts fresh.
	//
	// Trade-off: on the rare terminal that splits an OSC response
	// across two Read calls (ESC in one, `]11;...` in the next), the
	// leading `]NN;...` leaks through — but the tea.WithFilter
	// backstop (filterOSCResponses) catches those tail-shapes.
	if r.state == oscSawESC {
		if w < len(p) {
			p[w] = 0x1b
			w++
		}
		r.state = oscIdle
	}
	return w, err
}
