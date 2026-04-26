package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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

func TestMkdirAllNoSymlinkRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := mkdirAllNoSymlink(filepath.Join(link, "child"), 0o755)
	if err == nil {
		t.Fatal("mkdirAllNoSymlink should reject symlinked parent dirs")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "child")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}
