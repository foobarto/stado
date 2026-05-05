package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
		debounceLoop(ctx, events, rebuild, 50*time.Millisecond)
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
		debounceLoop(ctx, events, rebuild, 25*time.Millisecond)
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
		debounceLoop(ctx, events, rebuild, 100*time.Millisecond)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("debounceLoop did not exit on context cancel within 200ms")
	}
}

// TestRunDevWatchLoop_CleansUpOnContextCancel: starting the watch
// loop and immediately cancelling its context should cause it to
// remove the dev install dir + marker via deferred cleanup.
//
// This test does NOT exercise a real build — it simulates a state
// where PinActiveDev has run (creating the marker) and verifies
// CleanupDev fires on shutdown.
func TestRunDevWatchLoop_CleansUpOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.template.json"),
		[]byte(`{"name":"testplugin","version":"0.0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		var stdout, stderr bytes.Buffer
		_ = runDevWatchLoop(ctx, dir, &stdout, &stderr)
	}()

	// Wait for PinActiveDev to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	markerPath := filepath.Join(stateDir, "stado", "plugins", "active", "testplugin")
	for {
		if _, err := os.Stat(markerPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active marker never created")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should be cleaned up after cancel; stat err = %v", err)
	}
}
