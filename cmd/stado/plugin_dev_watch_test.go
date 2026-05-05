package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestDebounceLoop_CoalescesEvents: 5 events fired within the
// debounce window result in exactly 1 rebuild call.
func TestDebounceLoop_CoalescesEvents(t *testing.T) {
	events := make(chan struct{}, 10)
	var rebuilds int32
	rebuild := func() error { atomic.AddInt32(&rebuilds, 1); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		debounceLoop(ctx, events, rebuild, 50*time.Millisecond, nil)
	}()

	for i := 0; i < 5; i++ {
		events <- struct{}{}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&rebuilds); got != 1 {
		t.Errorf("rebuild count = %d, want 1", got)
	}

	cancel()
	<-done
}

// TestDebounceLoop_RebuildErrorContinues: rebuild returning error
// does not stop the loop; subsequent events still trigger rebuilds.
func TestDebounceLoop_RebuildErrorContinues(t *testing.T) {
	events := make(chan struct{}, 10)
	var rebuilds int32
	rebuild := func() error {
		atomic.AddInt32(&rebuilds, 1)
		return errors.New("simulated build failure")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		debounceLoop(ctx, events, rebuild, 25*time.Millisecond, nil)
	}()

	events <- struct{}{}
	time.Sleep(80 * time.Millisecond)
	events <- struct{}{}
	time.Sleep(80 * time.Millisecond)

	if got := atomic.LoadInt32(&rebuilds); got != 2 {
		t.Errorf("rebuild count = %d, want 2 (loop should survive build errors)", got)
	}

	cancel()
	<-done
}

// TestDebounceLoop_ExitsOnContextCancel: cancelling the context
// causes the loop to return promptly.
func TestDebounceLoop_ExitsOnContextCancel(t *testing.T) {
	events := make(chan struct{})
	rebuild := func() error { return nil }

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		debounceLoop(ctx, events, rebuild, 100*time.Millisecond, nil)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("debounceLoop did not exit on context cancel within 200ms")
	}
}
