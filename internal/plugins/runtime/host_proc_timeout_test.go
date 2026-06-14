package runtime

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"
)

// EP-0038c host_proc: stado_proc_read accepts timeout_ms; it used to be ignored
// (TODO in the code), so a read on a quiet subprocess blocked forever. These
// pin that the timeout is now applied to deadline-capable readers (the stdout
// pipe is an *os.File) while leaving deadline-less readers and the timeout==0
// (block) path unchanged.

// TestReadProcWithDeadline_TimesOutOnQuietPipe: a positive timeout bounds a read
// on a pipe with no data — it returns promptly with a deadline error instead of
// blocking. This is the behaviour the ignored param was supposed to provide.
func TestReadProcWithDeadline_TimesOutOnQuietPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	buf := make([]byte, 16)
	start := time.Now()
	n, rerr := readProcWithDeadline(r, buf, 50) // 50ms
	elapsed := time.Since(start)

	if rerr == nil {
		t.Fatalf("expected a timeout error on a quiet pipe; got n=%d, nil err", n)
	}
	if !errors.Is(rerr, os.ErrDeadlineExceeded) {
		t.Errorf("err = %v; want os.ErrDeadlineExceeded", rerr)
	}
	if elapsed > 2*time.Second {
		t.Errorf("read blocked %v despite 50ms timeout; timeout not applied", elapsed)
	}
}

// TestReadProcWithDeadline_ReadsAvailableDataWithTimeout: a positive timeout does
// not prevent reading data that is already available.
func TestReadProcWithDeadline_ReadsAvailableDataWithTimeout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	const payload = "hello"
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	buf := make([]byte, 16)
	n, rerr := readProcWithDeadline(r, buf, 1000)
	if rerr != nil {
		t.Fatalf("read of available data failed under timeout: %v", rerr)
	}
	if string(buf[:n]) != payload {
		t.Errorf("read %q; want %q", buf[:n], payload)
	}
}

// TestReadProcWithDeadline_DeadlineClearedAfterCall: after a timed-out read, the
// deadline is reset so a subsequent read on the same handle can still block /
// succeed (the import is called per-poll and must not poison the handle).
func TestReadProcWithDeadline_DeadlineClearedAfterCall(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	buf := make([]byte, 16)
	if _, rerr := readProcWithDeadline(r, buf, 30); !errors.Is(rerr, os.ErrDeadlineExceeded) {
		t.Fatalf("first read should time out; got %v", rerr)
	}
	// Write then read with no timeout — must succeed, proving the prior
	// deadline was cleared (a leftover past deadline would fail immediately).
	const payload = "later"
	go func() { _, _ = w.Write([]byte(payload)); _ = w.Close() }()
	n, rerr := readProcWithDeadline(r, buf, 0) // block
	if rerr != nil {
		t.Fatalf("blocking read after a timed-out read failed (deadline not cleared): %v", rerr)
	}
	if string(buf[:n]) != payload {
		t.Errorf("read %q; want %q", buf[:n], payload)
	}
}

// TestReadProcWithDeadline_DeadlinelessReaderIgnoresTimeout: a reader without
// SetReadDeadline (e.g. a bytes.Reader) reads normally even with a timeout set —
// the timeout is best-effort, not a hard requirement on the reader type.
func TestReadProcWithDeadline_DeadlinelessReaderIgnoresTimeout(t *testing.T) {
	r := bytes.NewReader([]byte("data"))
	buf := make([]byte, 16)
	n, rerr := readProcWithDeadline(r, buf, 10)
	if rerr != nil {
		t.Fatalf("deadlineless reader should read normally; got %v", rerr)
	}
	if string(buf[:n]) != "data" {
		t.Errorf("read %q; want %q", buf[:n], "data")
	}
}
