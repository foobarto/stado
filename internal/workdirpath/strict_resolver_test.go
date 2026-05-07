package workdirpath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 2.1.c tests for StrictResolver.

// ---- strict-from-/ -----------------------------------------------------

func TestStrictResolver_OpenRoot_RejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	// Plant a symlink mid-chain: base/realdir/...; base/link → realdir
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	s := NewStrictResolver()
	if _, err := s.OpenRoot(filepath.Join(link, "child")); err == nil {
		t.Fatal("expected symlink-component rejection, got nil")
	}
}

func TestStrictResolver_OpenRoot_AcceptsRealDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewStrictResolver()
	root, err := s.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	_ = root.Close()
}

func TestStrictResolver_MkdirAll_CreatesNestedDirs(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "a", "b", "c")
	s := NewStrictResolver()
	if err := s.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("not created: %v", err)
	}
}

func TestStrictResolver_OpenRegularFile_RejectsFinalSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real.txt")
	if err := os.WriteFile(real, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	s := NewStrictResolver()
	if _, err := s.OpenRegularFile(link); err == nil {
		t.Fatal("expected final-symlink rejection, got nil")
	}
}

func TestStrictResolver_ReadFileLimited_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStrictResolver()
	got, err := s.ReadFileLimited(target, 1024)
	if err != nil {
		t.Fatalf("ReadFileLimited: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("contents = %q", got)
	}
}

func TestStrictResolver_ReadFileLimited_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("more than the 4-byte cap"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStrictResolver()
	if _, err := s.ReadFileLimited(target, 4); err == nil {
		t.Fatal("expected oversize error, got nil")
	}
}

func TestStrictResolver_RemoveAll_RemovesTree(t *testing.T) {
	base := t.TempDir()
	tree := filepath.Join(base, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "a", "b", "x"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStrictResolver()
	if err := s.RemoveAll(tree); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("tree still exists: %v", err)
	}
}

func TestStrictResolver_RemoveAll_RejectsFinalSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	s := NewStrictResolver()
	if err := s.RemoveAll(link); err == nil {
		t.Fatal("expected symlink rejection, got nil")
	}
	// real should still exist (link wasn't removed; the real
	// dir definitely shouldn't have been touched).
	if _, err := os.Stat(real); err != nil {
		t.Errorf("real removed despite symlink rejection: %v", err)
	}
}

// ---- Under(ancestor) ---------------------------------------------------

func TestStrictResolver_Under_RejectsEmptyAncestor(t *testing.T) {
	s := NewStrictResolver()
	if _, err := s.Under(""); err == nil {
		t.Fatal("expected error on empty ancestor, got nil")
	}
}

func TestStrictResolver_Under_AcceptsAncestorSymlinkAbove(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "var", "home", "user")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlink: base/home → base/var/home
	if err := os.Symlink(filepath.Join(base, "var", "home"), filepath.Join(base, "home")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// Use the symlinked path as ancestor.
	ancestor := filepath.Join(base, "home", "user")

	s := NewStrictResolver()
	under, err := s.Under(ancestor)
	if err != nil {
		t.Fatalf("Under: %v", err)
	}

	// Strict-from-/ would reject the symlinked ancestor, but
	// Under accepts it (matching legacy *NoSymlinkUnder).
	if err := under.MkdirAll("subdir/nested", 0o755); err != nil {
		t.Fatalf("MkdirAll under symlinked ancestor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "subdir", "nested")); err != nil {
		t.Errorf("nested not created: %v", err)
	}
}

func TestStrictResolver_Under_RejectsSymlinkBelow(t *testing.T) {
	base := t.TempDir()
	ancestor := filepath.Join(base, "ancestor")
	if err := os.MkdirAll(ancestor, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink BELOW the ancestor:
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ancestor, "redirect")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	s := NewStrictResolver()
	under, err := s.Under(ancestor)
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	if _, err := under.OpenRoot("redirect/child"); err == nil {
		t.Fatal("expected symlink-below-ancestor rejection, got nil")
	}
}

func TestStrictResolver_Under_OpenRoot_AncestorEqualityNoOp(t *testing.T) {
	// path == "." or "" relative to ancestor opens the ancestor
	// itself; matches legacy semantics.
	dir := t.TempDir()
	s := NewStrictResolver()
	under, err := s.Under(dir)
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	root, err := under.OpenRoot(".")
	if err != nil {
		t.Fatalf("OpenRoot(.): %v", err)
	}
	_ = root.Close()
}

// ---- Methods unsupported on Under-derived resolver ---------------------

func TestStrictResolver_Under_OpenRegularFile_NotSupported(t *testing.T) {
	dir := t.TempDir()
	s := NewStrictResolver()
	under, _ := s.Under(dir)
	_, err := under.OpenRegularFile("data.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %q, want 'not supported'", err.Error())
	}
}

func TestStrictResolver_Under_ReadFileLimited_NotSupported(t *testing.T) {
	dir := t.TempDir()
	s := NewStrictResolver()
	under, _ := s.Under(dir)
	_, err := under.ReadFileLimited("data.txt", 1024)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %q, want 'not supported'", err.Error())
	}
}

func TestStrictResolver_Under_RemoveAll_NotSupported(t *testing.T) {
	dir := t.TempDir()
	s := NewStrictResolver()
	under, _ := s.Under(dir)
	err := under.RemoveAll("anything")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %q, want 'not supported'", err.Error())
	}
}

// ---- NUL-byte rejection ------------------------------------------------

func TestStrictResolver_RejectsNULByte(t *testing.T) {
	s := NewStrictResolver()
	bad := "/some/path\x00trailing"
	if _, err := s.OpenRoot(bad); err == nil {
		t.Error("OpenRoot: expected NUL rejection")
	}
	if err := s.MkdirAll(bad, 0o755); err == nil {
		t.Error("MkdirAll: expected NUL rejection")
	}
	if _, err := s.OpenRegularFile(bad); err == nil {
		t.Error("OpenRegularFile: expected NUL rejection")
	}
	if err := s.RemoveAll(bad); err == nil {
		t.Error("RemoveAll: expected NUL rejection")
	}
}
