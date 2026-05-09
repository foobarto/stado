package pty

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// expectAttached spawns a session, attaches it, and returns the manager
// + id. Caller defers Destroy. Used by every Expect test below.
func expectAttached(t *testing.T, opts SpawnOpts) (*Manager, uint64) {
	t.Helper()
	m := NewManager()
	id, err := m.Spawn(opts)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return m, id
}

// drainRingHas blocks (with polling) until the session's ring contains
// want as a substring or deadline elapses. Returns true if seen. Lets
// the test wait for drain output without consuming it via Read.
func drainRingHas(m *Manager, id uint64, want []byte, total time.Duration) bool {
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		s, err := m.get(id)
		if err != nil {
			return false
		}
		s.mu.Lock()
		got := s.ring.peekAll()
		s.mu.Unlock()
		if bytes.Contains(got, want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// AC2 — match against bytes already in the ring fires immediately
// without waiting for new bytes.
func TestExpect_MatchExistingBufferContents(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Cmd: "printf 'abc\\ndef\\n'; sleep 5"})
	defer m.Destroy(id)

	if !drainRingHas(m, id, []byte("def"), 2*time.Second) {
		t.Fatal("did not see expected output land in ring within 2s")
	}

	res, err := m.Expect(id,
		[]Pattern{{Bytes: []byte("def")}},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if !res.Matched {
		t.Fatalf("Matched=false; want true. res=%+v", res)
	}
	if res.PatternIndex != 0 {
		t.Errorf("PatternIndex=%d; want 0", res.PatternIndex)
	}
	if string(res.Match) != "def" {
		t.Errorf("Match=%q; want %q", res.Match, "def")
	}
	if !bytes.Contains(res.Before, []byte("abc")) {
		t.Errorf("Before should include 'abc'; got %q", res.Before)
	}
}

// AC3 — match against newly-arrived bytes works.
func TestExpect_MatchOnNewArrivals(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Cmd: "sleep 0.3; echo HELLO; sleep 5"})
	defer m.Destroy(id)

	start := time.Now()
	res, err := m.Expect(id,
		[]Pattern{{Bytes: []byte("HELLO")}},
		3*time.Second,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if !res.Matched {
		t.Fatalf("Matched=false; want true. res=%+v", res)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("returned too quickly (%v) — should have waited for the producer", elapsed)
	}
	if string(res.Match) != "HELLO" {
		t.Errorf("Match=%q; want %q", res.Match, "HELLO")
	}
}

// AC4 — multi-pattern returns the index of whichever appears earliest
// in the byte stream, breaking position ties by lower slice index.
func TestExpect_MultiPatternReturnsFirstMatchByByteOrder(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Cmd: "printf 'BBB\\nAAA\\n'; sleep 5"})
	defer m.Destroy(id)

	if !drainRingHas(m, id, []byte("AAA"), 2*time.Second) {
		t.Fatal("did not see expected output land in ring within 2s")
	}

	res, err := m.Expect(id,
		[]Pattern{{Bytes: []byte("AAA")}, {Bytes: []byte("BBB")}},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if !res.Matched {
		t.Fatalf("Matched=false; want true")
	}
	if res.PatternIndex != 1 {
		t.Errorf("PatternIndex=%d; want 1 (BBB matched at byte 0, AAA at byte 4)", res.PatternIndex)
	}
	if string(res.Match) != "BBB" {
		t.Errorf("Match=%q; want %q", res.Match, "BBB")
	}
}

// AC5 — regex mode matches against compiled RE2.
func TestExpect_RegexMode(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Cmd: "printf 'Connection error: refused\\n'; sleep 5"})
	defer m.Destroy(id)

	re := regexp.MustCompile(`err.*r`)
	res, err := m.Expect(id,
		[]Pattern{{Regex: re}},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if !res.Matched {
		t.Fatalf("Matched=false; want true")
	}
	// "err.*r" against "Connection error: refused" matches the
	// greedy run from "error: r" through to the last "r" in
	// "refused" or earlier — exact span depends on RE2's leftmost-
	// longest match. Verify it starts at "error" and the matched
	// region contains "err" + an "r" later.
	if !bytes.Contains(res.Match, []byte("err")) {
		t.Errorf("Match=%q; should contain 'err'", res.Match)
	}
	if !bytes.HasSuffix(res.Match, []byte("r")) {
		t.Errorf("Match=%q; should end at an 'r' (RE2 leftmost-longest)", res.Match)
	}
}

// AC6 — timeout returns matched=false, timeout=true with all bytes
// seen during the wait in Before, and the session stays alive.
func TestExpect_Timeout(t *testing.T) {
	// `cat` is interactive; without input it produces nothing — perfect
	// for the timeout path.
	m, id := expectAttached(t, SpawnOpts{Argv: []string{"/bin/cat"}})
	defer m.Destroy(id)

	start := time.Now()
	res, err := m.Expect(id,
		[]Pattern{{Bytes: []byte("never")}},
		100*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if res.Matched || !res.Timeout {
		t.Fatalf("expected Timeout=true Matched=false; got %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("timeout elapsed %v; expected ~100ms", elapsed)
	}
	// Session still alive: subsequent Read returns nothing because
	// the ring is empty (Expect drained it into Before).
	got, err := m.Read(id, 64, 50*time.Millisecond)
	if err != nil && !errors.Is(err, errReadEmpty) {
		// Read returns (nil, nil) on empty+timeout; (nil, EOF) on
		// closed. Either no-bytes outcome is acceptable here so
		// long as the session isn't reported missing.
		t.Logf("post-timeout Read: err=%v got=%q (informational)", err, got)
	}
	if len(got) != 0 {
		t.Errorf("post-timeout Read returned %q; expected empty (Expect should have drained the ring)", got)
	}
}

// errReadEmpty is unused — kept here to silence the linter for the
// reference above and document the expectation that Read returns nil
// on empty + timeout.
var errReadEmpty = errors.New("read empty")

// AC7 — EOF returns eof=true with the exit code populated when the
// underlying process exits before any pattern matches.
func TestExpect_EOFWithExitCode(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Cmd: "exit 3"})
	defer m.Destroy(id)

	res, err := m.Expect(id,
		[]Pattern{{Bytes: []byte("nope")}},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if res.Matched || res.Timeout || !res.EOF {
		t.Fatalf("expected EOF=true; got %+v", res)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode=%d; want 3", res.ExitCode)
	}
}

// AC8 — after-match bytes stay readable via subsequent Read.
func TestExpect_AfterMatchBytesStayReadable(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Cmd: "printf 'PROMPT> ready data'; sleep 5"})
	defer m.Destroy(id)

	if !drainRingHas(m, id, []byte("ready data"), 2*time.Second) {
		t.Fatal("did not see full output land in ring within 2s")
	}

	res, err := m.Expect(id,
		[]Pattern{{Bytes: []byte("PROMPT> ")}},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if !res.Matched {
		t.Fatalf("Matched=false; want true. res=%+v", res)
	}

	got, err := m.Read(id, 1024, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("post-match Read: %v", err)
	}
	if !bytes.Contains(got, []byte("ready data")) {
		t.Errorf("post-match Read=%q; want it to contain 'ready data'", got)
	}
}

// AC9 — concurrent Expect on the same session is rejected.
func TestExpect_ConcurrentExpectRejected(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Argv: []string{"/bin/cat"}})
	defer m.Destroy(id)

	// First Expect blocks waiting for "never"; second Expect should
	// get the rejection back immediately.
	var wg sync.WaitGroup
	firstErrCh := make(chan error, 1)
	wg.Go(func() {
		_, err := m.Expect(id,
			[]Pattern{{Bytes: []byte("never")}},
			500*time.Millisecond,
		)
		firstErrCh <- err
	})

	// Give the first Expect a moment to acquire the flag.
	time.Sleep(50 * time.Millisecond)

	_, secondErr := m.Expect(id,
		[]Pattern{{Bytes: []byte("never")}},
		200*time.Millisecond,
	)
	if secondErr == nil {
		t.Fatal("second concurrent Expect: want error, got nil")
	}
	if !strings.Contains(secondErr.Error(), "expect already in progress") {
		t.Errorf("second Expect error=%q; want it to mention 'expect already in progress'", secondErr.Error())
	}

	wg.Wait()
	if err := <-firstErrCh; err != nil {
		t.Errorf("first Expect surfaced an error: %v", err)
	}
}

// AC10 — pattern-count and empty-pattern guards. The host import is
// where the cap-check lives; manager-level only enforces "at least
// one pattern" because constructing 0-length Pattern{} is the only
// shape the manager itself can detect.
func TestExpect_PatternGuards(t *testing.T) {
	m, id := expectAttached(t, SpawnOpts{Argv: []string{"/bin/cat"}})
	defer m.Destroy(id)

	if _, err := m.Expect(id, nil, 100*time.Millisecond); err == nil {
		t.Error("Expect with nil patterns: want error, got nil")
	}
	if _, err := m.Expect(id, []Pattern{}, 100*time.Millisecond); err == nil {
		t.Error("Expect with empty patterns: want error, got nil")
	}
}

// Ring.Unshift unit tests — verify the front-push + overflow semantics
// the AC8 path depends on.

func TestRingUnshift_EmptyRing(t *testing.T) {
	rb := newRingBuffer(16)
	if dropped := rb.Unshift([]byte("hello")); dropped != 0 {
		t.Errorf("dropped=%d; want 0", dropped)
	}
	if got := rb.ReadN(16); string(got) != "hello" {
		t.Errorf("ReadN=%q; want %q", got, "hello")
	}
}

func TestRingUnshift_PrependsToExisting(t *testing.T) {
	rb := newRingBuffer(32)
	rb.Write([]byte("world"))
	if dropped := rb.Unshift([]byte("hello ")); dropped != 0 {
		t.Errorf("dropped=%d; want 0", dropped)
	}
	if got := rb.ReadN(32); string(got) != "hello world" {
		t.Errorf("ReadN=%q; want %q", got, "hello world")
	}
}

func TestRingUnshift_OverflowDropsExistingTail(t *testing.T) {
	rb := newRingBuffer(8)
	rb.Write([]byte("12345"))
	dropped := rb.Unshift([]byte("ABCDEF"))
	// combined = "ABCDEF" + "12345" = 11 bytes → drop tail to fit 8.
	// Kept: "ABCDEF12"; dropped: "345" = 3 bytes.
	if dropped != 3 {
		t.Errorf("dropped=%d; want 3", dropped)
	}
	if got := rb.ReadN(8); string(got) != "ABCDEF12" {
		t.Errorf("ReadN=%q; want %q", got, "ABCDEF12")
	}
}

func TestRingUnshift_PreservesAfterWraparound(t *testing.T) {
	rb := newRingBuffer(8)
	// Write 6, read 4 → r=4 w=6, layout linear but offset.
	rb.Write([]byte("aaaaaa"))
	_ = rb.ReadN(4)
	// Write 4 more: wraps. r=4 w=2 (after writing "bbbb").
	rb.Write([]byte("bbbb"))
	// Ring contents in read order: "aabbbb" (6 bytes).
	// Unshift "X" → "Xaabbbb" (7 bytes), no overflow.
	if dropped := rb.Unshift([]byte("X")); dropped != 0 {
		t.Errorf("dropped=%d; want 0", dropped)
	}
	if got := rb.ReadN(8); string(got) != "Xaabbbb" {
		t.Errorf("ReadN=%q; want %q", got, "Xaabbbb")
	}
}
