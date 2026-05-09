package pty

import "time"

// ringBuffer is a fixed-capacity byte ring. Writes that would exceed
// capacity discard oldest bytes (terminal-scrollback semantics) and
// return the number of bytes dropped. ReadN consumes from the oldest
// end. Not safe for concurrent use — callers (the session) hold a
// mutex around it.
type ringBuffer struct {
	buf  []byte
	r, w int
	full bool
}

func newRingBuffer(capBytes int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, capBytes)}
}

// Len reports the number of unread bytes.
func (b *ringBuffer) Len() int {
	if b.full {
		return len(b.buf)
	}
	if b.w >= b.r {
		return b.w - b.r
	}
	return len(b.buf) - b.r + b.w
}

// Cap reports buffer capacity.
func (b *ringBuffer) Cap() int { return len(b.buf) }

// Write appends p, dropping oldest bytes on overflow. Returns the
// count of bytes dropped (0 in the common case).
func (b *ringBuffer) Write(p []byte) uint64 {
	cap := len(b.buf)
	if cap == 0 || len(p) == 0 {
		return 0
	}

	// If p is larger than the whole ring, only the tail-cap bytes of
	// p will fit; the rest is dropped before they ever land.
	dropped := uint64(0)
	if len(p) > cap {
		dropped += uint64(len(p) - cap)
		p = p[len(p)-cap:]
	}

	for _, c := range p {
		b.buf[b.w] = c
		b.w = (b.w + 1) % cap
		if b.full {
			// Overwriting unread byte — advance r and account.
			b.r = (b.r + 1) % cap
			dropped++
		}
		if b.w == b.r {
			b.full = true
		}
	}
	return dropped
}

// ReadN consumes up to n bytes from the oldest end. Returns nil when
// empty.
func (b *ringBuffer) ReadN(n int) []byte {
	avail := b.Len()
	if avail == 0 || n <= 0 {
		return nil
	}
	if n > avail {
		n = avail
	}
	out := make([]byte, n)
	cap := len(b.buf)
	for i := 0; i < n; i++ {
		out[i] = b.buf[b.r]
		b.r = (b.r + 1) % cap
	}
	b.full = false
	return out
}

// Unshift pushes p onto the FRONT of the ring so subsequent ReadN sees
// it before any existing unread bytes. Used by Expect to put the post-
// match tail back after consuming bytes for pattern matching.
//
// Overflow semantics: if len(p)+Len() > Cap(), drop bytes from the END
// of the combined sequence (the tail of the existing buffered data).
// p is preserved up to Cap(); existing data shifts right and the
// trailing bytes that don't fit are discarded. Returns the count of
// dropped bytes. Mirrors Write's "newer wins" intuition: the bytes
// being put back ARE the newest from the caller's perspective (they
// were just observed), so they're kept even when the ring is small.
//
// Caller holds s.mu (same as Write/ReadN).
func (b *ringBuffer) Unshift(p []byte) uint64 {
	cap := len(b.buf)
	if cap == 0 || len(p) == 0 {
		return 0
	}
	existing := b.peekAll()
	combined := make([]byte, 0, len(p)+len(existing))
	combined = append(combined, p...)
	combined = append(combined, existing...)
	dropped := uint64(0)
	if len(combined) > cap {
		dropped = uint64(len(combined) - cap)
		combined = combined[:cap]
	}
	// Reset to a linear layout starting at offset 0 — simpler than
	// trying to preserve wraparound across an Unshift, and Unshift is
	// not on the hot path (one call per matched Expect).
	copy(b.buf, combined)
	b.r = 0
	b.w = len(combined) % cap
	b.full = len(combined) == cap
	return dropped
}

// peekAll snapshots the unread bytes in read order without consuming.
// Caller holds s.mu.
func (b *ringBuffer) peekAll() []byte {
	n := b.Len()
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	cap := len(b.buf)
	rr := b.r
	for i := 0; i < n; i++ {
		out[i] = b.buf[rr]
		rr = (rr + 1) % cap
	}
	return out
}

// condWaitTimeout blocks the session's cond up to d. Wakes early on
// any Broadcast. Caller must hold s.mu.
func (s *session) condWaitTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.AfterFunc(d, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer timer.Stop()
	s.cond.Wait()
}
