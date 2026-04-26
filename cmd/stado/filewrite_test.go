package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type fakeSyncedWriteCloser struct {
	buf      bytes.Buffer
	syncErr  error
	closeErr error
	closed   bool
}

func (f *fakeSyncedWriteCloser) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *fakeSyncedWriteCloser) Sync() error                 { return f.syncErr }
func (f *fakeSyncedWriteCloser) Close() error {
	f.closed = true
	return f.closeErr
}

func TestCopyAndCloseFile_PropagatesCloseError(t *testing.T) {
	out := &fakeSyncedWriteCloser{closeErr: errors.New("close failed")}
	err := copyAndCloseFile(out, bytes.NewBufferString("hello"))
	if err == nil || err.Error() != "close failed" {
		t.Fatalf("copyAndCloseFile close err = %v, want close failed", err)
	}
	if !out.closed {
		t.Fatal("copyAndCloseFile did not close writer")
	}
	if got := out.buf.String(); got != "hello" {
		t.Fatalf("copied bytes = %q, want hello", got)
	}
}

func TestCopyAndCloseFile_PropagatesSyncError(t *testing.T) {
	out := &fakeSyncedWriteCloser{syncErr: errors.New("sync failed")}
	err := copyAndCloseFile(out, bytes.NewBufferString("hello"))
	if err == nil || err.Error() != "sync failed" {
		t.Fatalf("copyAndCloseFile sync err = %v, want sync failed", err)
	}
	if !out.closed {
		t.Fatal("copyAndCloseFile did not close writer after sync failure")
	}
}

func TestCopyAndCloseFileLimitedRejectsOversizedReader(t *testing.T) {
	out := &fakeSyncedWriteCloser{}
	err := copyAndCloseFileLimited(out, bytes.NewBufferString("hello"), 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("copyAndCloseFileLimited err = %v, want size rejection", err)
	}
	if !out.closed {
		t.Fatal("copyAndCloseFileLimited did not close writer after size rejection")
	}
	if got := out.buf.String(); got != "hell" {
		t.Fatalf("copied bytes = %q, want capped prefix", got)
	}
}
