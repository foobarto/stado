package pty

import (
	"errors"
	"sync"
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
//
// Also covers the codex critique: the original test only called
// CloseAll serially. Concurrent CloseAll must also not panic on
// "close of closed channel" — the m.mu lock around the
// select-against-stopWatchdog block is what makes that safe; this
// test exercises it.
func TestWatchdog_StopsOnCloseAll(t *testing.T) {
	t.Run("serial double-close", func(t *testing.T) {
		m := NewManagerWithOpts(ManagerOpts{
			IdleTimeout:  50 * time.Millisecond,
			WatchdogTick: 10 * time.Millisecond,
		})
		m.CloseAll()
		m.CloseAll() // idempotent
	})

	t.Run("concurrent CloseAll does not panic", func(t *testing.T) {
		m := NewManagerWithOpts(ManagerOpts{
			IdleTimeout:  50 * time.Millisecond,
			WatchdogTick: 10 * time.Millisecond,
		})
		// Spawn a session so CloseAll has real work to do.
		_, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		// Fire 8 concurrent CloseAll calls. Without the m.mu-guarded
		// select-default in CloseAll, the second and later closers
		// would hit "close of closed channel" and panic.
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.CloseAll()
			}()
		}
		wg.Wait()
	})
}

// TestWatchdog_NoisyOrphansGetReaped: a session producing PTY output
// without any client touching it IS the orphan we want to reap. A
// crashed-client scenario with a still-spinning child process should
// NOT keep the daemon pinned. The codex+gemini second-pass review
// caught that the original "drain-as-touch" design defeated the
// watchdog's whole point: a hung loop emitting progress noise stayed
// alive forever.
//
// Setup: spawn a sh loop that echoes every 20 ms; never attach,
// never touch. With a 50 ms idle timeout, the watchdog should reap
// the session within ~100-200 ms despite the drain output.
func TestWatchdog_NoisyOrphansGetReaped(t *testing.T) {
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
	// Don't attach, don't touch. Wait long enough for the watchdog
	// to fire several times. We deliberately don't use Snapshot
	// (that would touch); poll List instead and look for the session
	// disappearing from it.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		found := false
		for _, info := range m.List() {
			if info.ID == id {
				found = true
				break
			}
		}
		if !found {
			return // expected — orphan reaped despite drain noise
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("noisy orphan never reaped within 500ms — drain output should NOT refresh the orphan clock")
}

// TestWatchdog_ClosedUnattachedReapedImmediately: a child that
// exited stays in the registry until idleTimeout in the original
// design. Gemini caught it: closed-AND-unattached sessions are
// corpses pinning the daemon for no reason. They should be reaped
// regardless of idleTimeout, on the next watchdog tick.
func TestWatchdog_ClosedUnattachedReapedImmediately(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  10 * time.Hour, // far past anything we'd wait for
		WatchdogTick: 10 * time.Millisecond,
	})
	defer m.CloseAll()

	// `true` exits immediately. Don't attach. The reap goroutine
	// marks closed=true; combined with attached=false, the watchdog
	// reaps next tick despite the 10-hour idle timeout.
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		found := false
		for _, info := range m.List() {
			if info.ID == id {
				found = true
				break
			}
		}
		if !found {
			return // expected
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("closed-unattached session not reaped within 500ms despite 10h idleTimeout")
}

// TestWatchdog_ReadDoesNotGetKilledMidWait: codex caught that Read
// only touches when bytes return; a client blocked in Read for the
// timeout could be reaped while waiting. Now Read touches on entry
// AND on each cond.Wait wake.
func TestWatchdog_ReadDoesNotGetKilledMidWait(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  100 * time.Millisecond,
		WatchdogTick: 10 * time.Millisecond,
	})
	defer m.CloseAll()

	// `sleep 1` produces no output. A client blocked in Read should
	// stay alive even though no drain or other client touch occurs.
	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/sleep", "1"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Block in Read for 300 ms — three idleTimeouts.
	_, err = m.Read(id, 4096, 300*time.Millisecond)
	if err == ErrNotFound {
		t.Errorf("Read mid-wait was killed by watchdog; codex's TOCTOU bug")
	}
	// Session should still be reachable after the read returns.
	if _, err := m.Snapshot(id); err == ErrNotFound {
		t.Errorf("session unexpectedly reaped after Read returned")
	}
}
