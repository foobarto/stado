package limitedio

import (
	"strings"
	"testing"
)

func TestBufferCapsStoredBytes(t *testing.T) {
	buf := NewBuffer(4)
	n, err := buf.Write([]byte("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("Write count = %d, want 6", n)
	}
	if got := buf.String(); got != "abcd" {
		t.Fatalf("stored = %q, want abcd", got)
	}
	if !buf.Truncated() {
		t.Fatal("expected truncation flag")
	}
}

func TestBufferHandlesIncrementalWrites(t *testing.T) {
	buf := NewBuffer(5)
	_, _ = buf.Write([]byte("ab"))
	_, _ = buf.Write([]byte("cdef"))
	if got := buf.String(); got != "abcde" {
		t.Fatalf("stored = %q, want abcde", got)
	}
	if !buf.Truncated() {
		t.Fatal("expected truncation flag")
	}
	if strings.TrimSpace(buf.String()) != "abcde" {
		t.Fatalf("unexpected buffer string: %q", buf.String())
	}
}
