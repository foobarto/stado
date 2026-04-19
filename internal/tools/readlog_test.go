package tools

import (
	"sync"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

func TestReadLogRoundTrip(t *testing.T) {
	l := NewReadLog()
	key := tool.ReadKey{Path: "a.go", Range: "1:10"}

	if _, ok := l.PriorRead(key); ok {
		t.Fatal("unexpected prior read on empty log")
	}

	l.RecordRead(key, tool.PriorReadInfo{ContentHash: "deadbeef"})
	info, ok := l.PriorRead(key)
	if !ok {
		t.Fatal("expected prior read after RecordRead")
	}
	if info.ContentHash != "deadbeef" {
		t.Fatalf("hash: %q", info.ContentHash)
	}
	if info.Turn != 1 {
		t.Fatalf("auto-stamp turn: got %d want 1", info.Turn)
	}
}

// TestReadLogTurnAutoStamp asserts the log fills in PriorReadInfo.Turn
// when the caller passes zero (the common case).
func TestReadLogTurnAutoStamp(t *testing.T) {
	l := NewReadLog()
	l.BumpTurn()
	l.BumpTurn() // turn = 3

	key := tool.ReadKey{Path: "x"}
	l.RecordRead(key, tool.PriorReadInfo{ContentHash: "h"})
	info, _ := l.PriorRead(key)
	if info.Turn != 3 {
		t.Fatalf("expected turn 3, got %d", info.Turn)
	}
}

// TestReadLogPreservesCallerTurn asserts a non-zero Turn passed in is kept
// as-is — lets replay harnesses inject a turn value.
func TestReadLogPreservesCallerTurn(t *testing.T) {
	l := NewReadLog()
	key := tool.ReadKey{Path: "x"}
	l.RecordRead(key, tool.PriorReadInfo{ContentHash: "h", Turn: 42})
	info, _ := l.PriorRead(key)
	if info.Turn != 42 {
		t.Fatalf("caller Turn was overwritten: got %d want 42", info.Turn)
	}
}

// TestReadLogConcurrentMutex asserts the log doesn't corrupt under
// concurrent RecordRead + PriorRead.
func TestReadLogConcurrentMutex(t *testing.T) {
	l := NewReadLog()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			l.RecordRead(tool.ReadKey{Path: "p", Range: string(rune('A' + i%26))},
				tool.PriorReadInfo{ContentHash: "h"})
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _ = l.PriorRead(tool.ReadKey{Path: "p", Range: string(rune('A' + i%26))})
		}(i)
	}
	wg.Wait()
	// If we got here without the race detector screaming, we're good.
}

// TestNullHostSatisfiesInterface is a compile-time contract check;
// the var _ tool.Host = NullHost{} assertion in readlog.go handles
// the compile-time part. This test exercises the runtime behaviour.
func TestNullHostBehaviour(t *testing.T) {
	var h NullHost
	if _, ok := h.PriorRead(tool.ReadKey{}); ok {
		t.Fatal("NullHost.PriorRead must return ok=false")
	}
	// RecordRead must not panic.
	h.RecordRead(tool.ReadKey{}, tool.PriorReadInfo{})
}
