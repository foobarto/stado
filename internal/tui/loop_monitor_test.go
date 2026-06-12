package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/tui/theme"
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
			lines = append(lines, v.line)
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
	go streamMonitor(ctx, send, `echo first; sleep 2; echo second`, 1)

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
		streamMonitor(ctx, send, `while true; do echo x; sleep 0.1; done`, 1)
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

// TestMonitorStaleDoneIgnoredForNewInstance reproduces the instance race codex
// flagged on #135: after `/monitor stop` kills monitor A and a new `/monitor`
// starts as B, A's cancelled goroutine still delivers its monitorDoneMsg. The
// old onMonitorDone only nil-checked m.monitor, so the stale completion cleared
// B (orphaning its process) and falsely reported it exited. The generation tag
// must make the stale done a no-op while B's own completion still clears it.
func TestMonitorStaleDoneIgnoredForNewInstance(t *testing.T) {
	m := &Model{rootCtx: context.Background(), theme: theme.Default()}

	// Start A (gen 1); ignore the returned start Cmd so no real process spawns.
	m.handleMonitorCmd("echo a")
	genA := m.monitor.gen
	// Stop A — m.monitor goes nil.
	m.handleMonitorCmd("stop")
	if m.monitor != nil {
		t.Fatal("monitor not cleared after /monitor stop")
	}
	// Start B (fresh generation).
	m.handleMonitorCmd("echo b")
	if m.monitor == nil || m.monitor.gen == genA {
		t.Fatalf("monitor B did not start with a fresh generation (genA=%d, monitor=%+v)", genA, m.monitor)
	}
	genB := m.monitor.gen
	nBlocks := len(m.blocks)

	// A's stale completion arrives now that B is active.
	onMonitorDone(m, monitorDoneMsg{gen: genA})
	if m.monitor == nil {
		t.Fatal("stale done from monitor A cleared the active monitor B")
	}
	if m.monitor.gen != genB {
		t.Fatalf("active monitor changed after stale done: got gen %d, want %d", m.monitor.gen, genB)
	}
	if len(m.blocks) != nBlocks {
		t.Fatalf("stale done appended a spurious block: %q", m.blocks[len(m.blocks)-1].body)
	}

	// B's own completion still clears it.
	onMonitorDone(m, monitorDoneMsg{gen: genB})
	if m.monitor != nil {
		t.Fatal("monitor B's own done should clear the active monitor")
	}
}
