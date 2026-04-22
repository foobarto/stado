package workdirpath

import (
	"os"
	"path/filepath"
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

func TestGlob_RejectsEscapePattern(t *testing.T) {
	root := t.TempDir()
	if _, err := Glob(root, "../*"); err == nil {
		t.Fatal("expected escape pattern to fail")
	}
}
