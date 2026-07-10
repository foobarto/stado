package plugins_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
)

func TestLockRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin-lock.toml")

	l := plugins.NewLock()
	l.Add(plugins.LockEntry{
		Identity:    "github.com/foo/bar@v1.0.0",
		WASMSHA256:  "abc123",
		AnchorFpr:   "deadbeef",
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	})

	if err := l.Write(path); err != nil {
		t.Fatal(err)
	}

	l2, err := plugins.ReadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l2.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(l2.Entries))
	}
	if got := l2.Entries[0].Identity; got != "github.com/foo/bar@v1.0.0" {
		t.Errorf("identity: got %q", got)
	}
	if got := l2.Entries[0].WASMSHA256; got != "abc123" {
		t.Errorf("wasm_sha256: got %q", got)
	}
}

func TestLockMissingFile(t *testing.T) {
	_, err := plugins.ReadLock("/nonexistent/plugin-lock.toml")
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist, got: %v", err)
	}
}

func TestLockAddUpdate(t *testing.T) {
	l := plugins.NewLock()
	l.Add(plugins.LockEntry{Identity: "github.com/foo/bar@v1.0.0", WASMSHA256: "aaa"})
	l.Add(plugins.LockEntry{Identity: "github.com/foo/bar@v1.0.0", WASMSHA256: "bbb"})
	if len(l.Entries) != 1 {
		t.Errorf("duplicate Add should update, not append; got %d entries", len(l.Entries))
	}
	if l.Entries[0].WASMSHA256 != "bbb" {
		t.Errorf("updated sha256 should be 'bbb', got %q", l.Entries[0].WASMSHA256)
	}
}

func TestLockGet(t *testing.T) {
	l := plugins.NewLock()
	l.Add(plugins.LockEntry{Identity: "github.com/foo/bar@v1.0.0", WASMSHA256: "abc"})
	e, ok := l.Get("github.com/foo/bar@v1.0.0")
	if !ok || e.WASMSHA256 != "abc" {
		t.Errorf("Get returned %v, ok=%v", e, ok)
	}
	_, ok = l.Get("github.com/foo/other@v1.0.0")
	if ok {
		t.Error("Get should return false for missing entry")
	}
}

func TestLockReadWriteRejectSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugin-lock.toml")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := plugins.ReadLock(path); err == nil {
		t.Fatal("ReadLock followed a symlink")
	}
	if err := plugins.NewLock().Write(path); err == nil {
		t.Fatal("Lock.Write replaced or followed a symlink")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sentinel" {
		t.Fatalf("outside file changed through lock symlink: %q", got)
	}
}
