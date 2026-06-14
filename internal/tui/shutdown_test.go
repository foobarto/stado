package tui

import (
	"context"
	"testing"
	"time"

	rt "github.com/foobarto/stado/internal/runtime"
)

// TestShutdown_CancelsRunningFleet regresses EP-0034: the TUI spawns
// background fleet agents off a non-cancellable root context (RootContext
// discards its cancel) and never called Fleet.CancelAll on exit, so quitting
// the UI left subagent goroutines — and the provider calls / child processes
// they drive — running orphaned. Model.Shutdown (deferred by app.Run) must
// cancel them.
func TestShutdown_CancelsRunningFleet(t *testing.T) {
	fleet := rt.NewFleet()
	sp := &fakeSpawner{delay: 10 * time.Second} // stays 'running'
	id, err := fleet.Spawn(context.Background(), sp, "bg-task", rt.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = fleet.Cancel(id) })

	m := &Model{fleet: fleet}

	// Sanity: entry is running before shutdown.
	if e, _ := fleet.Get(id); e.Status != rt.FleetStatusRunning {
		t.Fatalf("pre-shutdown status = %q; want running", e.Status)
	}

	m.Shutdown()

	deadline := time.Now().Add(2 * time.Second)
	for {
		e, ok := fleet.Get(id)
		if !ok {
			t.Fatalf("entry %s vanished", id)
		}
		if e.Status == rt.FleetStatusCancelled {
			return // success
		}
		if time.Now().After(deadline) {
			t.Fatalf("after Shutdown, status = %q; want cancelled (CancelAll not wired)", e.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestShutdown_NilFleet: Shutdown must be safe when no fleet was ever created
// (e.g. a session that never spawned a background agent).
func TestShutdown_NilFleet(t *testing.T) {
	m := &Model{}
	m.Shutdown() // must not panic
}
