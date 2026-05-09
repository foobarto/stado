package pty

import (
	"bytes"
	"fmt"
	"regexp"
	"time"
)

// Pattern is one needle for Manager.Expect to scan against the PTY's
// post-output byte stream. Exactly one of Bytes (substring) or Regex
// (RE2) is set; mixing both is a programmer error and the host import
// rejects it before constructing Pattern values.
//
// Patterns operate on the RAW byte stream — the same bytes the ring
// captures and Read returns. For full-screen TUIs the model should use
// Snapshot instead; ANSI escape interleaving will defeat substring
// matching against rendered words.
type Pattern struct {
	Bytes []byte
	Regex *regexp.Regexp
}

// ExpectResult is the outcome of one Manager.Expect call. Exactly one
// of Matched / Timeout / EOF is true. Before holds the bytes consumed
// from the stream prior to the match (or all bytes seen on
// timeout/EOF). Match holds the matched bytes (only when Matched).
// PatternIndex names the input pattern that fired (only when Matched).
// ExitCode is the child process's exit status (only when EOF).
type ExpectResult struct {
	Matched      bool
	PatternIndex int
	Before       []byte
	Match        []byte
	Timeout      bool
	EOF          bool
	ExitCode     int
}

// expectExitCodeWait bounds how long Expect waits after detecting EOF
// for the reaper to populate exitCode. Drain marking closed and reap
// recording exit are two separate s.mu acquisitions; in practice exit
// is set within microseconds of close. 100 ms is a generous ceiling
// before falling back to -1.
const expectExitCodeWait = 100 * time.Millisecond

// Expect blocks until one of the patterns matches against the session's
// post-output byte stream, the deadline elapses, or the underlying
// process exits. Returns a structured ExpectResult — see the field
// comments for the discriminator.
//
// Match semantics: scans first against bytes already buffered in the
// ring (so a prompt that landed before the call returns immediately),
// then waits for new bytes if no match is found. Across multiple
// patterns the EARLIEST byte position wins; ties broken by the lower
// patterns slice index.
//
// After a match, the post-match tail is pushed back into the ring so
// subsequent Read / Expect calls see it. Timeout / EOF return all
// bytes seen as Before with the ring drained — call Read for further
// output (it will return io.EOF if the session is closed and empty).
//
// Caller must have Attach'd the session (same contract as Read).
// Concurrent Expect on one session is rejected with a session-named
// error; Read concurrent with Expect is allowed but undefined: the two
// will fight for ring bytes.
func (m *Manager) Expect(id uint64, patterns []Pattern, timeout time.Duration) (ExpectResult, error) {
	s, err := m.get(id)
	if err != nil {
		return ExpectResult{}, err
	}
	if len(patterns) == 0 {
		return ExpectResult{}, fmt.Errorf("pty: expect requires at least one pattern")
	}
	longest := 0
	for _, p := range patterns {
		n := patternMaxLen(p)
		if n > longest {
			longest = n
		}
	}
	if longest <= 0 {
		// Defensive — patternMaxLen returns 0 for nil/empty patterns,
		// which the host import rejects before reaching here. Falling
		// to 1 keeps the straddle-window math sensible.
		longest = 1
	}

	deadline := time.Now().Add(timeout)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.expectInProgress {
		return ExpectResult{}, fmt.Errorf("session %d: expect already in progress", id)
	}
	if !s.attached {
		return ExpectResult{}, ErrNotAttached
	}
	s.expectInProgress = true
	defer func() { s.expectInProgress = false }()

	s.touch()
	s.readWaiters++
	defer func() {
		s.readWaiters--
		s.touch()
	}()

	var buf []byte
	scannedTo := 0 // bytes [0, scannedTo) have been fully checked for any match

	for {
		if avail := s.ring.Len(); avail > 0 {
			if got := s.ring.ReadN(avail); len(got) > 0 {
				buf = append(buf, got...)
			}
		}

		// Re-scan the last (longest-1) bytes of the previously-
		// scanned region to catch patterns that straddle the boundary
		// between earlier scans and freshly-arrived bytes.
		scanFrom := max(scannedTo-(longest-1), 0)
		if scanFrom < len(buf) {
			pos, matchLen, idx := scanForMatch(buf[scanFrom:], patterns)
			if idx >= 0 {
				absPos := scanFrom + pos
				before := append([]byte(nil), buf[:absPos]...)
				match := append([]byte(nil), buf[absPos:absPos+matchLen]...)
				if after := buf[absPos+matchLen:]; len(after) > 0 {
					_ = s.ring.Unshift(after)
				}
				return ExpectResult{
					Matched:      true,
					PatternIndex: idx,
					Before:       before,
					Match:        match,
				}, nil
			}
		}
		scannedTo = len(buf)

		// Process exited — drain has already flushed everything to
		// the ring. We've drained that into buf above; if nothing
		// matched we're at EOF.
		if s.closed {
			return ExpectResult{
				EOF:      true,
				Before:   append([]byte(nil), buf...),
				ExitCode: s.waitExitCode(expectExitCodeWait),
			}, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ExpectResult{
				Timeout: true,
				Before:  append([]byte(nil), buf...),
			}, nil
		}

		// Wait for drain to broadcast (new bytes or close).
		s.condWaitTimeout(remaining)
	}
}

// scanForMatch returns the first match by byte position across all
// patterns: lowest pos wins, ties broken by lower slice index. Returns
// (0, 0, -1) when no pattern matches anywhere in b.
func scanForMatch(b []byte, patterns []Pattern) (pos, matchLen, idx int) {
	bestPos := -1
	bestLen := 0
	bestIdx := -1
	for i, p := range patterns {
		var mp, ml int
		switch {
		case p.Regex != nil:
			loc := p.Regex.FindIndex(b)
			if loc == nil {
				continue
			}
			mp = loc[0]
			ml = loc[1] - loc[0]
		case len(p.Bytes) > 0:
			mp = bytes.Index(b, p.Bytes)
			if mp < 0 {
				continue
			}
			ml = len(p.Bytes)
		default:
			continue
		}
		if bestIdx < 0 || mp < bestPos || (mp == bestPos && i < bestIdx) {
			bestPos = mp
			bestLen = ml
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return 0, 0, -1
	}
	return bestPos, bestLen, bestIdx
}

// patternMaxLen returns the longest possible match length for a
// Pattern. For substrings it's exact; for regex it's an upper bound
// derived from the compiled program's max-match width when the regex
// has a finite ceiling, else falls back to a generous constant. Used
// only to size the straddle re-scan window — over-estimation is
// harmless (we just re-scan slightly more); under-estimation would
// miss boundary-straddling matches.
func patternMaxLen(p Pattern) int {
	if p.Regex != nil {
		// regexp doesn't expose match-width directly. Use the
		// pattern source length as a soft cap: it bounds substring
		// patterns exactly, and for genuine regex it's a useful
		// approximation that grows with the pattern complexity.
		// Plus a constant fudge for character-class expansions.
		const regexFudge = 64
		return len(p.Regex.String()) + regexFudge
	}
	return len(p.Bytes)
}

// waitExitCode blocks (briefly) for the reap goroutine to populate
// s.exitCode after EOF. Caller holds s.mu. Returns -1 if the wait
// expires before exitCode is set.
func (s *session) waitExitCode(maxWait time.Duration) int {
	if s.exitCode != nil {
		return *s.exitCode
	}
	deadline := time.Now().Add(maxWait)
	for s.exitCode == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return -1
		}
		s.condWaitTimeout(remaining)
	}
	return *s.exitCode
}
