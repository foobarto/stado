package pty

import (
	"testing"
	"time"
)

// TestSnapshotVersion_BumpsOnDrainOutput: each chunk of bytes the
// PTY emits bumps session.version. Locks the contract polling
// consumers depend on for cheap "did anything change" checks.
func TestSnapshotVersion_BumpsOnDrainOutput(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Fresh session: no drain output yet → version 0.
	v0, err := m.SnapshotVersion(id)
	if err != nil {
		t.Fatalf("SnapshotVersion: %v", err)
	}
	if v0 != 0 {
		t.Errorf("fresh session version = %d, want 0", v0)
	}

	// Write to cat's stdin → cat echoes → drain bumps version.
	if _, err := m.Write(id, []byte("ping1\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := m.SnapshotVersion(id)
		if v > v0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	v1, _ := m.SnapshotVersion(id)
	if v1 <= v0 {
		t.Errorf("after first write+echo: version = %d, expected > %d", v1, v0)
	}

	// Second write → version bumps again.
	if _, err := m.Write(id, []byte("ping2\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for time.Now().Before(deadline) {
		v, _ := m.SnapshotVersion(id)
		if v > v1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	v2, _ := m.SnapshotVersion(id)
	if v2 <= v1 {
		t.Errorf("after second write+echo: version = %d, expected > %d", v2, v1)
	}
}

// TestSnapshotIfChanged_NoChangeReturnsNil: when sinceVersion ==
// current version, return (nil, sameVersion, nil) — caller's existing
// frame is still authoritative. The fast path that lets the TUI's
// 30 Hz tick stop allocating cell grids on idle sessions.
func TestSnapshotIfChanged_NoChangeReturnsNil(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// First snapshot — get the current version.
	scr, err := m.Snapshot(id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	currentVersion := scr.Version

	// Same-version peek: no allocation, returns nil frame.
	frame, ver, err := m.SnapshotIfChanged(id, currentVersion)
	if err != nil {
		t.Fatalf("SnapshotIfChanged unchanged: %v", err)
	}
	if frame != nil {
		t.Errorf("SnapshotIfChanged with sinceVersion=current returned non-nil frame; should skip allocation")
	}
	if ver != currentVersion {
		t.Errorf("SnapshotIfChanged version = %d, want %d", ver, currentVersion)
	}
}

// TestSnapshotIfChanged_ChangedReturnsFrame: when sinceVersion is
// stale, return the current screen + new version. Locks the "yes
// re-render needed" path.
func TestSnapshotIfChanged_ChangedReturnsFrame(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := m.Attach(id, AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := m.Write(id, []byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Wait for drain to bump version.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := m.SnapshotVersion(id)
		if v > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Pretend caller hasn't seen any version yet (sinceVersion=0).
	frame, ver, err := m.SnapshotIfChanged(id, 0)
	if err != nil {
		t.Fatalf("SnapshotIfChanged: %v", err)
	}
	if frame == nil {
		t.Fatal("SnapshotIfChanged with stale sinceVersion: want frame, got nil")
	}
	if ver == 0 {
		t.Errorf("returned version should be > 0; got 0")
	}
	if frame.Version != ver {
		t.Errorf("Screen.Version = %d, returned ver = %d; should match", frame.Version, ver)
	}
}

// TestSnapshotVersion_DoesNotTouchClient: SnapshotVersion is a
// read-only peek and should NOT count as a client touch — otherwise
// a polling consumer using it would keep an otherwise-orphan
// session alive against the watchdog.
func TestSnapshotVersion_DoesNotTouchClient(t *testing.T) {
	m := NewManagerWithOpts(ManagerOpts{
		IdleTimeout:  50 * time.Millisecond,
		WatchdogTick: 10 * time.Millisecond,
	})
	defer m.CloseAll()

	id, err := m.Spawn(SpawnOpts{Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Poll SnapshotVersion every 20ms for ~200ms. With a 50ms idle
	// timeout, the session should still be reaped because the peeks
	// don't count as client touches. Without this property, polling
	// agents would inadvertently extend session life forever.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, _ = m.SnapshotVersion(id) // ignore error; checking touch behaviour
		time.Sleep(20 * time.Millisecond)
		// Look for the session disappearing from List (= reaped).
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
	}
	t.Errorf("SnapshotVersion polling kept the session alive — should be a non-touching peek")
}
