package workdirpath

import (
	"os"
	"path/filepath"
	"testing"
)

// Phase 2.1.a tests for the new RootResolver type. The contracts
// to assert are:
//
//   1. Independent construction — no Resolver required.
//   2. Borrowed handle — RootResolver never closes the *os.Root.
//   3. Nil tolerance — methods on a nil-root resolver return
//      typed errors, not panics.
//   4. Symlink + non-regular rejection — inherited from the
//      legacy primitives (preserved through the wrapping).

// ---- Construction ------------------------------------------------------

func TestNewRootResolver_AcceptsValidRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	rr := NewRootResolver(root)
	if rr == nil {
		t.Fatal("NewRootResolver returned nil")
	}
	if rr.Root() != root {
		t.Error("Root() did not return the wrapped *os.Root")
	}
}

func TestNewRootResolver_AcceptsNilRoot(t *testing.T) {
	// Nil-tolerant constructor; methods surface the error at
	// call time. Mirrors legacy Root* functions.
	rr := NewRootResolver(nil)
	if rr == nil {
		t.Fatal("constructor returned nil for nil root")
	}
	if _, err := rr.ReadFileLimited("anything", 1024); err == nil {
		t.Error("expected error on nil root, got nil")
	}
	if err := rr.MkdirAll("anything", 0o755); err == nil {
		t.Error("MkdirAll on nil root should error")
	}
}

// ---- Borrowed-handle contract ------------------------------------------

func TestNewRootResolver_DoesNotCloseHandle(t *testing.T) {
	// The caller owns the *os.Root. Even if the RootResolver
	// goes out of scope (no explicit Close on it), the handle
	// must remain usable.
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	rr := NewRootResolver(root)
	if err := rr.MkdirAll("subdir", 0o755); err != nil {
		t.Fatalf("MkdirAll via resolver: %v", err)
	}

	// Drop the resolver; the handle must still work.
	rr = nil
	_ = rr // silence unused
	if err := root.Mkdir("siblingdir", 0o755); err != nil {
		t.Fatalf("direct *os.Root use after resolver drop: %v", err)
	}
}

// ---- ReadFileLimited ---------------------------------------------------

func TestRootResolver_ReadFileLimited_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	got, err := rr.ReadFileLimited("data.txt", 1024)
	if err != nil {
		t.Fatalf("ReadFileLimited: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("read = %q, want payload", got)
	}
}

func TestRootResolver_ReadFileLimited_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"),
		[]byte("more bytes than the limit"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	if _, err := rr.ReadFileLimited("big.txt", 4); err == nil {
		t.Fatal("expected oversize error, got nil")
	}
}

func TestRootResolver_ReadFileLimited_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	if _, err := rr.ReadFileLimited("link.txt", 1024); err == nil {
		t.Fatal("expected symlink rejection, got nil")
	}
}

// ---- WriteFileAtomic ---------------------------------------------------

func TestRootResolver_WriteFileAtomic_PreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	// Write with 0o644 perm; existing file is 0o600 — preserve
	// existing.
	if err := rr.WriteFileAtomic("data.txt", []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0o600 (preserved)", info.Mode().Perm())
	}
}

func TestRootResolver_WriteFileAtomicExactMode_ReplacesMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	if err := rr.WriteFileAtomicExactMode("data.txt", []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomicExactMode: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 0o644 (replaced)", info.Mode().Perm())
	}
}

// ---- MkdirAll ----------------------------------------------------------

func TestRootResolver_MkdirAll_CreatesNestedDirs(t *testing.T) {
	dir := t.TempDir()
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	if err := rr.MkdirAll("a/b/c", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b", "c")); err != nil {
		t.Errorf("nested dir not created: %v", err)
	}
}

func TestRootResolver_MkdirAll_RejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	rr := NewRootResolver(root)

	if err := rr.MkdirAll("/etc", 0o755); err == nil {
		t.Fatal("expected error on absolute path, got nil")
	}
}
