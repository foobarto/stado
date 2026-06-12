package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestMonitorStreamsLive reproduces EP-0036's core /monitor contract:
// "each stdout line → system block in the current session." The original
// implementation buffered every line and returned them as a single batch
// only after the process exited (cmd.Wait), so a long-running monitor
// (tail -f, ping) produced ZERO output until it terminated. This test
// asserts the first line is delivered while the process is still running.
func TestMonitorStreamsLive(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	gotFirst := make(chan struct{})
	done := make(chan struct{})

	send := func(msg tea.Msg) {
		switch v := msg.(type) {
		case monitorLineMsg:
			mu.Lock()
			lines = append(lines, string(v))
			if len(lines) == 1 {
				close(gotFirst)
			}
			mu.Unlock()
		case monitorDoneMsg:
			close(done)
		}
	}

	// Emit one line immediately, then sleep 2s, then a second line.
	// If output streams, the first line arrives within ~1s; if it
	// buffers until exit, nothing arrives for >2s.
	ctx := context.Background()
	go streamMonitor(ctx, send, `echo first; sleep 2; echo second`)

	select {
	case <-gotFirst:
		// streamed in time — good
	case <-time.After(1 * time.Second):
		t.Fatal("first monitor line not delivered within 1s — output is buffered, not streamed")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor never signalled done")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
		t.Fatalf("expected [first second] in order, got %v", lines)
	}
}

// TestMonitorCancelStops verifies a cancelled context terminates the
// monitored process and signals done, even for an otherwise unbounded
// command (the /monitor stop path).
func TestMonitorCancelStops(t *testing.T) {
	send := func(tea.Msg) {}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		streamMonitor(ctx, send, `while true; do echo x; sleep 0.1; done`)
		close(doneCh)
	}()
	// Let it spin briefly, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("streamMonitor did not return after context cancel")
	}
}

// TestLoopStopWithoutActiveLoop reproduces the misleading-feedback defect:
// `/loop stop` with no active loop reported "loop stopped" (a lie), unlike
// `/monitor stop` which correctly reports "no active monitor".
func TestLoopStopWithoutActiveLoop(t *testing.T) {
	m := &Model{}
	m.handleLoopCmd("stop")
	if len(m.blocks) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(m.blocks))
	}
	got := m.blocks[0].body
	if got == "loop stopped" {
		t.Fatalf("`/loop stop` with no active loop falsely reported %q; expected a no-active-loop notice", got)
	}
}
