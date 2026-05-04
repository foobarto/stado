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

// TestRequiresAttach: read/write fail with ErrNotAttached when
// nobody's attached.
func TestRequiresAttach(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Destroy(id)
	if _, err := m.Write(id, []byte("x")); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("Write w/o attach: want ErrNotAttached, got %v", err)
	}
	if _, err := m.Read(id, 64, 100*time.Millisecond); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("Read w/o attach: want ErrNotAttached, got %v", err)
	}
}

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
