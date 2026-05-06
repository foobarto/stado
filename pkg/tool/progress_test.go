package tool

import (
	"context"
	"sync"
	"testing"
)

func TestProgressCollector_AppendDrain(t *testing.T) {
	pc := &ProgressCollector{}
	pc.Append("p1", "first")
	pc.Append("p1", "second")
	got := pc.Drain()
	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Errorf("drain: got %+v", got)
	}
	// Drain a second time → empty.
	if got := pc.Drain(); len(got) != 0 {
		t.Errorf("expected empty re-drain, got %+v", got)
	}
}

func TestProgressCollector_BoundedRing(t *testing.T) {
	pc := &ProgressCollector{}
	for i := 0; i < ProgressCollectorMax+10; i++ {
		pc.Append("p", "x")
	}
	got := pc.Drain()
	if len(got) != ProgressCollectorMax {
		t.Errorf("buffer should cap at %d; got %d", ProgressCollectorMax, len(got))
	}
}

func TestProgressCollector_NilSafe(t *testing.T) {
	var pc *ProgressCollector
	pc.Append("p", "x") // must not panic
	if got := pc.Drain(); got != nil {
		t.Error("nil drain should return nil")
	}
}

func TestProgressCollector_Concurrent(t *testing.T) {
	pc := &ProgressCollector{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				pc.Append("p", "x")
			}
		}()
	}
	wg.Wait()
	got := pc.Drain()
	// Each of 10 goroutines appended 20 = 200; capped at
	// ProgressCollectorMax (64). The dropping semantics may have
	// trimmed entries — just confirm cap is honoured and no panic.
	if len(got) > ProgressCollectorMax {
		t.Errorf("over-cap: %d > %d", len(got), ProgressCollectorMax)
	}
}

func TestContextWithProgress_RoundTrip(t *testing.T) {
	ctx, pc := ContextWithProgress(context.Background())
	if pc == nil {
		t.Fatal("nil collector")
	}
	got := ProgressFromContext(ctx)
	if got != pc {
		t.Errorf("collector lookup mismatch")
	}
	// Bare context returns nil.
	if got := ProgressFromContext(context.Background()); got != nil {
		t.Errorf("bare ctx should return nil; got %+v", got)
	}
	// Nil context returns nil safely.
	if got := ProgressFromContext(nil); got != nil {
		t.Errorf("nil ctx should return nil; got %+v", got)
	}
}
