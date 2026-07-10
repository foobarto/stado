package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLockWriteRejectsUnreadableOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-lock.toml")
	lock := plugins.NewLock()
	lock.Add(plugins.LockEntry{Identity: strings.Repeat("x", 5<<20)})
	if err := lock.Write(path); err == nil {
		t.Fatal("Lock.Write produced a file larger than ReadLock accepts")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized lock should not be written: %v", err)
	}
}

func TestLockRemove(t *testing.T) {
	lock := plugins.NewLock()
	lock.Add(plugins.LockEntry{Identity: "old"})
	lock.Add(plugins.LockEntry{Identity: "keep"})
	if !lock.Remove("old") || lock.Remove("missing") {
		t.Fatal("Lock.Remove returned the wrong result")
	}
	if len(lock.Entries) != 1 || lock.Entries[0].Identity != "keep" {
		t.Fatalf("entries after remove = %#v", lock.Entries)
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
