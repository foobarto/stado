package runtime

import "testing"

// TestBoundedAlloc covers the clamp policy that protects the host
// from guest-controlled OOM. Three cases matter operationally:
//
//   - Negative n: a guest sending a math.MinInt32 cast must not panic
//     (make([]byte, negative) panics in Go). boundedAlloc returns nil
//     so the caller's "no buffer → return 0" branch handles it.
//
//   - Zero n: degenerate but legal; boundedAlloc returns nil, caller
//     short-circuits.
//
//   - Above ceiling: clamped to maxHostBufferBytes (16 MiB) rather
//     than allocated as requested. Plugin sees a short read instead
//     of crashing the host.
//
// Locking these behaviours in matters because each is the right shape
// for a different threat model: the negative case is "buggy plugin",
// the zero case is "edge case", the above-ceiling case is "DoS attempt".
func TestBoundedAlloc(t *testing.T) {
	cases := []struct {
		name    string
		n       int32
		wantLen int
	}{
		{"negative", -1, 0},
		{"min int32", -2147483648, 0},
		{"zero", 0, 0},
		{"small", 1024, 1024},
		{"at ceiling", 16 << 20, 16 << 20},
		{"above ceiling clamped", 32 << 20, 16 << 20},
		{"max int32 clamped", 2147483647, 16 << 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf := boundedAlloc(c.n)
			if len(buf) != c.wantLen {
				t.Errorf("boundedAlloc(%d) len = %d, want %d", c.n, len(buf), c.wantLen)
			}
			// Returned buffer length and capacity must match — caller
			// reads up to len(buf), and a buffer with cap > len would
			// hide read-into-extra-slack bugs.
			if cap(buf) != len(buf) {
				t.Errorf("boundedAlloc(%d) cap=%d != len=%d", c.n, cap(buf), len(buf))
			}
		})
	}
}
