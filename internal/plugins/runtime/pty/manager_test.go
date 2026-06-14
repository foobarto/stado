package pty

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSpawnReadDestroy: bash -c 'echo hi' lands "hi" in the ring,
// reaper records exit 0, Destroy is idempotent.
func TestSpawnReadDestroy(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Cmd: "echo hi"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got := readUntil(t, m, id, []byte("hi"), 2*time.Second)
	if !bytes.Contains(got, []byte("hi")) {
		t.Fatalf("read: want 'hi' substring, got %q", got)
	}
	// Wait for child exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if infos := m.List(); len(infos) == 1 && !infos[0].Alive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.Destroy(id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := m.Destroy(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Destroy(repeat): want ErrNotFound, got %v", err)
	}
}

// TestWriteReadInteractive: cat -- write a line, expect echo back.
func TestWriteReadInteractive(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := m.Write(id, []byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readUntil(t, m, id, []byte("hello"), 2*time.Second)
	if !bytes.Contains(got, []byte("hello")) {
		t.Fatalf("read: want 'hello' echo, got %q", got)
	}
}

// TestSpawnDescriptionInList (EP-0043 D8): a spawn description is stored
// and surfaced in List for session identification.
func TestSpawnDescriptionInList(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Description: "tail prod logs"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	infos := m.List()
	if len(infos) != 1 {
		t.Fatalf("List len = %d, want 1", len(infos))
	}
	if infos[0].Description != "tail prod logs" {
		t.Fatalf("List description = %q, want %q", infos[0].Description, "tail prod logs")
	}
}

// TestWriteReadWithoutAttach (EP-0043 D6): read/write no longer require
// an explicit Attach — spawn then write/read works directly. This is the
// "you need to attach first" friction the lock removal eliminates.
func TestWriteReadWithoutAttach(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	// No Attach call.
	if _, err := m.Write(id, []byte("nolock\n")); err != nil {
		t.Fatalf("Write without attach: %v", err)
	}
	got := readUntil(t, m, id, []byte("nolock"), 2*time.Second)
	if !bytes.Contains(got, []byte("nolock")) {
		t.Fatalf("read without attach: want 'nolock' echo, got %q", got)
	}
}

// TestAttachContention: a session can only have one attacher; force
// steals it.
func TestAttachContention(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)

	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); !errors.Is(err, ErrAlreadyAttached) {
		t.Fatalf("second Attach: want ErrAlreadyAttached, got %v", err)
	}
	if err := m.Attach(id, AttachOpts{Force: true}); err != nil {
		t.Fatalf("force Attach: %v", err)
	}
}

// TestDetachKeepsRunning: detach, kid keeps producing, ring captures
// it; reattach replays the captured bytes.
func TestDetachKeepsRunning(t *testing.T) {
	m := NewManager()
	// `printf` then sleep — produces output once, then idles, so the
	// test isn't timing-sensitive about exit ordering.
	id, err := m.Spawn(SpawnOpts{Cmd: "printf detached; sleep 5"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	// Detach is set to false at spawn — attach + immediately detach
	// to exercise the path.
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := m.Detach(id); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	// Wait for output to land in the ring buffer while detached.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if infos := m.List(); len(infos) == 1 && infos[0].Buffered > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("re-Attach: %v", err)
	}
	got := readUntil(t, m, id, []byte("detached"), 2*time.Second)
	if !bytes.Contains(got, []byte("detached")) {
		t.Fatalf("read after re-attach: want 'detached', got %q", got)
	}
}

// TestSignalCtrlC: signal a sleeping cat with SIGTERM, verify the
// reaper records non-zero exit.
func TestSignalCtrlC(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	if err := m.Signal(id, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if infos := m.List(); len(infos) == 1 && !infos[0].Alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session did not exit after SIGTERM within 2s")
}

// TestRingBufferOverflow: write more than capacity, oldest bytes drop.
func TestRingBufferOverflow(t *testing.T) {
	rb := newRingBuffer(8)
	if dropped := rb.Write([]byte("12345678")); dropped != 0 {
		t.Fatalf("first write dropped=%d, want 0", dropped)
	}
	if rb.Len() != 8 {
		t.Fatalf("len=%d, want 8", rb.Len())
	}
	dropped := rb.Write([]byte("ABC"))
	if dropped != 3 {
		t.Fatalf("overflow dropped=%d, want 3", dropped)
	}
	got := rb.ReadN(8)
	if string(got) != "45678ABC" {
		t.Fatalf("ring contents=%q, want %q", got, "45678ABC")
	}
}

// TestDiscardPending: DiscardPending drops the buffered ring bytes (returning
// the count) so a subsequent Read sees none of them. Backs the read tool's
// mode:"auto" screen drain (EP-0043) — see registerPTYSnapshot.
func TestDiscardPending(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Cmd: "printf discard-me; sleep 5"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)

	// Wait for the printf to land in the ring.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if infos := m.List(); len(infos) == 1 && infos[0].Buffered >= len("discard-me") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	n, err := m.DiscardPending(id)
	if err != nil {
		t.Fatalf("DiscardPending: %v", err)
	}
	if n < len("discard-me") {
		t.Fatalf("DiscardPending dropped %d bytes, want >= %d", n, len("discard-me"))
	}

	// The ring is now empty: a short non-blocking Read returns nothing.
	got, err := m.Read(id, 4096, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Read after discard: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Read after discard returned %q, want empty (ring was drained)", got)
	}

	// And a second discard with an empty ring reports zero, no error.
	if n2, err := m.DiscardPending(id); err != nil || n2 != 0 {
		t.Fatalf("DiscardPending on empty ring = (%d, %v), want (0, nil)", n2, err)
	}
}

// TestDiscardPendingSkipsActiveConsumer: the auto-screen drain must not steal
// bytes from a concurrent Read/Expect that's mid-flight (Codex/review P1). When
// readWaiters>0 or expectInProgress, DiscardPending is a no-op and the ring is
// preserved; once the consumer is gone it drains. Set the flags directly (we're
// in-package) because a real blocked reader would consume the ring the instant
// data arrives, leaving no deterministic window to observe.
func TestDiscardPendingSkipsActiveConsumer(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Cmd: "printf keep-me; sleep 5"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	s, err := m.get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Wait for the printf to land in the ring.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := s.ring.Len()
		s.mu.Unlock()
		if n >= len("keep-me") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Each guard flag, in turn, makes DiscardPending a no-op that preserves
	// the buffered bytes.
	for _, tc := range []struct {
		name string
		set  func()
		clr  func()
	}{
		{"readWaiters", func() { s.mu.Lock(); s.readWaiters++; s.mu.Unlock() }, func() { s.mu.Lock(); s.readWaiters--; s.mu.Unlock() }},
		{"expectInProgress", func() { s.mu.Lock(); s.expectInProgress = true; s.mu.Unlock() }, func() { s.mu.Lock(); s.expectInProgress = false; s.mu.Unlock() }},
	} {
		tc.set()
		n, err := m.DiscardPending(id)
		if err != nil {
			t.Fatalf("[%s] DiscardPending: %v", tc.name, err)
		}
		if n != 0 {
			t.Errorf("[%s] DiscardPending drained %d bytes with a consumer active, want 0", tc.name, n)
		}
		s.mu.Lock()
		buffered := s.ring.Len()
		s.mu.Unlock()
		if buffered < len("keep-me") {
			t.Errorf("[%s] ring lost data under the guard: buffered=%d, want >= %d", tc.name, buffered, len("keep-me"))
		}
		tc.clr()
	}

	// With no consumer, the drain proceeds.
	n, err := m.DiscardPending(id)
	if err != nil {
		t.Fatalf("DiscardPending (no consumer): %v", err)
	}
	if n < len("keep-me") {
		t.Fatalf("DiscardPending with no consumer dropped %d, want >= %d", n, len("keep-me"))
	}
}

// (EP-0043 D6: the old TestRequiresAttach was removed — read/write no
// longer require attach, and the ErrNotAttached sentinel is gone.
// TestWriteReadWithoutAttach covers the new contract.)

// TestCloseAll terminates pending sessions.
func TestCloseAll(t *testing.T) {
	m := NewManager()
	for i := 0; i < 3; i++ {
		if _, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}}); err != nil {
			t.Fatalf("Spawn[%d]: %v", i, err)
		}
	}
	if got := len(m.List()); got != 3 {
		t.Fatalf("List len=%d, want 3", got)
	}
	m.CloseAll()
	if got := len(m.List()); got != 0 {
		t.Fatalf("after CloseAll: List len=%d, want 0", got)
	}
}

// readUntil drains the session's ring with short timeouts until want
// is found in the accumulated output, or 2s elapses.
func readUntil(t *testing.T, m *Manager, id uint64, want []byte, total time.Duration) []byte {
	t.Helper()
	var acc []byte
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		got, err := m.Read(id, 4096, 200*time.Millisecond)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Read: %v", err)
		}
		acc = append(acc, got...)
		if bytes.Contains(acc, want) {
			return acc
		}
		if errors.Is(err, io.EOF) && !bytes.Contains(acc, want) {
			t.Fatalf("read EOF before seeing %q (got %q)", want, acc)
		}
	}
	return acc
}

// silence unused-import warning when building without all helpers.
var _ = strings.Contains
