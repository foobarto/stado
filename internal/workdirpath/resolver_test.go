package workdirpath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 2.1.a tests for the new Resolver type. Focus is the
// security boundary: any path that escapes the workdir via
// symlink redirect, NUL injection, or relative-traversal must
// fail closed.
//
// These tests intentionally exercise the same invariants the
// legacy `Resolve` / `OpenReadFile` / `WriteFile` tests assert,
// but through the new API. Once 2.1.d flips the dependency
// (legacy → wrappers around Resolver), this file is the primary
// test surface.

// ---- New / Workdir() ---------------------------------------------------

func TestNew_RejectsEmptyWorkdir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error on empty workdir, got nil")
	}
}

func TestNew_AcceptsRelativeWorkdirAndCanonicalises(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	r, err := New("./sub")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := r.Workdir()
	if !filepath.IsAbs(got) {
		t.Errorf("Workdir() = %q, want absolute", got)
	}
}

func TestNew_AcceptsNonExistentWorkdir(t *testing.T) {
	// Construction succeeds; method calls surface the missing
	// path at use time. Matches legacy semantics.
	r, err := New("/nonexistent/path")
	if err != nil {
		t.Fatalf("New on nonexistent: %v", err)
	}
	if r.Workdir() != "/nonexistent/path" {
		t.Errorf("Workdir() = %q, want /nonexistent/path", r.Workdir())
	}
	if _, err := r.Resolve("file.txt"); err == nil {
		t.Error("Resolve on nonexistent workdir should error")
	}
}

// ---- Resolve / ResolveAllowMissing -------------------------------------

func TestResolver_Resolve_RejectsEscapeViaSymlink(t *testing.T) {
	wk := t.TempDir()
	r := mustNew(t, wk)

	out := filepath.Join(filepath.Dir(wk), "outside")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	link := filepath.Join(wk, "escape")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	if _, err := r.Resolve("escape"); err == nil {
		t.Fatal("expected escape error, got nil")
	}
}

func TestResolver_Resolve_AcceptsValidNestedPath(t *testing.T) {
	wk := t.TempDir()
	nested := filepath.Join(wk, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(nested, "file.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := mustNew(t, wk)
	got, err := r.Resolve("a/b/c/file.txt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(got, "a/b/c/file.txt") &&
		!strings.HasSuffix(got, filepath.Join("a", "b", "c", "file.txt")) {
		t.Errorf("Resolve = %q, want suffix a/b/c/file.txt", got)
	}
}

func TestResolver_ResolveAllowMissing_AllowsCreatePath(t *testing.T) {
	wk := t.TempDir()
	r := mustNew(t, wk)

	// Path doesn't exist; ResolveAllowMissing lets it through
	// for create flow.
	got, err := r.ResolveAllowMissing("new/dir/file.txt")
	if err != nil {
		t.Fatalf("ResolveAllowMissing: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("new", "dir", "file.txt")) {
		t.Errorf("ResolveAllowMissing = %q, want suffix new/dir/file.txt", got)
	}

	// Strict Resolve on the same path errors.
	if _, err := r.Resolve("new/dir/file.txt"); err == nil {
		t.Error("Resolve on missing path should error")
	}
}

// ---- OpenRegularFile ---------------------------------------------------

func TestResolver_OpenRegularFile_RejectsSymlinkEscape(t *testing.T) {
	wk := t.TempDir()
	out := filepath.Join(filepath.Dir(wk), "secret")
	if err := os.WriteFile(out, []byte("sensitive"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(wk, "leak")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	r := mustNew(t, wk)
	f, err := r.OpenRegularFile("leak")
	if err == nil {
		_ = f.Close()
		t.Fatal("expected error opening symlink-escape path, got nil")
	}
}

func TestResolver_OpenRegularFile_ReadsRegularFile(t *testing.T) {
	wk := t.TempDir()
	target := filepath.Join(wk, "data.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := mustNew(t, wk)

	f, err := r.OpenRegularFile("data.txt")
	if err != nil {
		t.Fatalf("OpenRegularFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	if string(buf[:n]) != "hello" {
		t.Errorf("read = %q, want hello", buf[:n])
	}
}

// ---- WriteFileAtomic ---------------------------------------------------

func TestResolver_WriteFileAtomic_CreatesFileUnderWorkdir(t *testing.T) {
	wk := t.TempDir()
	r := mustNew(t, wk)

	if err := r.WriteFileAtomic("output.txt", []byte("payload"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wk, "output.txt"))
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("contents = %q", got)
	}
}

func TestResolver_WriteFileAtomic_RejectsParentSymlinkEscape(t *testing.T) {
	wk := t.TempDir()
	out := filepath.Join(filepath.Dir(wk), "victim")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(wk, "redirected")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := mustNew(t, wk)

	if err := r.WriteFileAtomic("redirected/leaked.txt",
		[]byte("escape"), 0o644); err == nil {
		t.Fatal("expected error writing through symlink, got nil")
	}
	// The file must NOT exist outside the workdir.
	if _, err := os.Stat(filepath.Join(out, "leaked.txt")); err == nil {
		t.Error("file leaked through parent-symlink escape")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat = %v, want NotExist", err)
	}
}

// ---- Glob --------------------------------------------------------------

func TestResolver_Glob_RejectsAbsolutePattern(t *testing.T) {
	r := mustNew(t, t.TempDir())
	if _, err := r.Glob("/etc/*"); err == nil {
		t.Fatal("expected error on absolute glob, got nil")
	}
}

func TestResolver_Glob_RejectsParentTraversalPattern(t *testing.T) {
	r := mustNew(t, t.TempDir())
	if _, err := r.Glob("../*"); err == nil {
		t.Fatal("expected error on `..` glob, got nil")
	}
}

func TestResolver_GlobLimited_TotalAndStoredCounts(t *testing.T) {
	wk := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(wk, "file"+string(rune('a'+i)))
		if err := os.WriteFile(name, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := mustNew(t, wk)
	matches, total, err := r.GlobLimited("file*", 3)
	if err != nil {
		t.Fatalf("GlobLimited: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("matches len = %d, want 3 (capped)", len(matches))
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
}

// ---- Helpers -----------------------------------------------------------

func mustNew(t *testing.T, workdir string) *Resolver {
	t.Helper()
	r, err := New(workdir)
	if err != nil {
		t.Fatalf("New(%q): %v", workdir, err)
	}
	return r
}
