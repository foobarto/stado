package pty

import (
	"errors"
	"testing"
	"time"
)

// TestWatchdog_DisabledByDefault: NewManager (no opts) keeps the
// pre-watchdog behaviour — sessions stay alive forever until explicit
// destroy. Backward-compat for callers that haven't opted in.
func TestWatchdog_DisabledByDefault(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Wait longer than any reasonable watchdog tick; session must
	// still be alive.
	time.Sleep(150 * time.Millisecond)
	if _, err := m.Snapshot(id); err != nil {
		t.Errorf("session should still be reachable without watchdog; got %v", err)
	}
}

// TestWatchdog_DestroysIdleSession: with a 50 ms idle timeout and
// 10 ms tick, a session that's seen no activity for >50 ms gets
// destroyed by the watchdog. The Snapshot after waiting fails with
// ErrNotFound — the load-bearing assertion that the orphan was
// reaped. Without this, an orphan PTY pins the daemon's idle-exit
// indefinitely.
func TestWatchdog_DestroysIdleSession(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  50 * time.Millisecond,
		WatchdogTick: 10 * time.Millisecond,
	})
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Wait long enough for the watchdog to notice. Two ticks past
	// the idle deadline gives plenty of slack on a slow CI runner.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, err := m.Snapshot(id)
		if errors.Is(err, ErrNotFound) {
			return // expected
		}
		// Important: don't keep snapshotting at full speed — Snapshot
		// touches the session, which would reset the idle clock and
		// the watchdog would never fire. Only check periodically.
		time.Sleep(75 * time.Millisecond)
	}
	t.Errorf("watchdog never destroyed idle session within 500ms")
}

// TestWatchdog_KeepsActiveSessionAlive: a session that's regularly
// touched (writes, reads, snapshots) MUST NOT be killed by the
// watchdog even when the idle timeout is short. Writers are the most
// representative active path — that's what an agent driving the PTY
// looks like.
func TestWatchdog_KeepsActiveSessionAlive(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  50 * time.Millisecond,
		WatchdogTick: 10 * time.Millisecond,
	})
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Touch every 20 ms for 200 ms. Idle timeout is 50 ms; if the
	// touch path doesn't update lastTouched, the watchdog will
	// destroy the session and the next Write fails.
	stop := time.After(200 * time.Millisecond)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	writes := 0
loop:
	for {
		select {
		case <-stop:
			break loop
		case <-tick.C:
			if _, err := m.Write(id, []byte("ping ")); err != nil {
				t.Fatalf("after %d writes, Write failed: %v — watchdog killed an active session", writes, err)
			}
			writes++
		}
	}
	if writes < 5 {
		t.Errorf("expected ≥5 successful writes; got %d", writes)
	}
	// Session should still be reachable.
	if _, err := m.Snapshot(id); err != nil {
		t.Errorf("active session unexpectedly gone: %v", err)
	}
}

// TestWatchdog_StopsOnCloseAll: CloseAll signals the watchdog to
// exit; subsequent operations don't trigger a panic via "send on
// closed channel" or similar. Locks the goroutine-lifecycle contract.
func TestWatchdog_StopsOnCloseAll(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  50 * time.Millisecond,
		WatchdogTick: 10 * time.Millisecond,
	})
	m.CloseAll()
	// Idempotent: double-close shouldn't panic.
	m.CloseAll()
}

// TestWatchdog_DrainOutputCountsAsTouch: a session producing PTY
// output (e.g., a long-running script) should NOT be reaped just
// because no client is touching it. Drain output is a sign the
// process is alive and the operator might be reading the snapshot
// later.
//
// The setup: spawn `sh -c 'while sleep 0.02; do echo .; done'` which
// emits output every 20 ms. With a 50 ms idle timeout, the drain
// touches keep lastTouched fresh; no client touch needed.
func TestWatchdog_DrainOutputCountsAsTouch(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  50 * time.Millisecond,
		WatchdogTick: 10 * time.Millisecond,
	})
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{
		Argv: []string{"/bin/sh", "-c", "while sleep 0.02; do echo .; done"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Don't attach; don't touch from the client side. Wait long
	// enough for several watchdog ticks. If drain output isn't
	// counted as a touch, the session would be reaped and Snapshot
	// would fail. (We use Snapshot rather than List to verify the
	// session is reachable — but Snapshot itself touches; that's
	// fine because we only check once at the end.)
	time.Sleep(300 * time.Millisecond)
	if _, err := m.Snapshot(id); err != nil {
		t.Errorf("output-producing session was reaped despite drain activity: %v", err)
	}
}
