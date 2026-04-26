package workdirpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve_RejectsEscapeViaSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "link.txt", false); err == nil {
		t.Fatal("expected symlink escape to fail")
	}
}

func TestResolve_AllowsNestedMissingPathInsideWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(root, filepath.Join("sub", "new", "file.txt"), true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(root, "sub", "new", "file.txt")
	if got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
}

func TestRootRel_ReturnsConfinedRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	gotRoot, gotRel, err := RootRel(root, filepath.Join(root, "sub", "file.txt"), true)
	if err != nil {
		t.Fatalf("RootRel: %v", err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if gotRel != filepath.Join("sub", "file.txt") {
		t.Fatalf("rel = %q", gotRel)
	}
}

func TestReadFileRejectsSymlinkEscapeAtOpen(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	_, err := ReadFile(root, "link.txt")
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("ReadFile error = %v, want workdir escape", err)
	}
}

func TestWriteFileRejectsSymlinkParentEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	err := WriteFile(root, filepath.Join("linkdir", "out.txt"), []byte("pwned"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("WriteFile error = %v, want workdir escape", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "out.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside write occurred, stat err = %v", statErr)
	}
}

func TestWriteFileRejectsInRootFinalSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := WriteFile(root, "link.txt", []byte("pwned"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("WriteFile error = %v, want symlink rejection", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "target" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestWriteFileCreatesNestedMissingPathInsideWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, filepath.Join("sub", "new", "file.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sub", "new", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("file content = %q, want ok", data)
	}
}

func TestWriteFilePreservesExistingMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(root, "script.sh", []byte("#!/bin/sh\necho ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %04o, want 0755", got)
	}
}

func TestGlob_RejectsEscapePattern(t *testing.T) {
	root := t.TempDir()
	if _, err := Glob(root, "../*"); err == nil {
		t.Fatal("expected escape pattern to fail")
	}
}
