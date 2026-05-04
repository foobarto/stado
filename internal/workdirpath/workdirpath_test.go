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

// TestResolve_RelativeWorkdirIsNotEscape regresses the v0.26.0
// release-build failure: Go 1.25+ filepath.EvalSymlinks(".") returns
// "." (preserves relative-input shape) instead of the absolute
// canonical path earlier Go versions returned. Resolve / RootRel
// / RootRelForWrite then misidentified relative resolved paths as
// "escapes workdir" because the prefix-confinement check assumed
// root was absolute. Pre-Abs was added to all three to fix.
//
// fetch-binaries.go calls Resolve via RootRelForWrite with workdir="."
// — that's the operator-facing reproduction. This test exercises
// the same shape directly.
func TestResolve_RelativeWorkdirIsNotEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	got, err := Resolve(".", filepath.Join("sub", "new", "file.txt"), true)
	if err != nil {
		t.Fatalf("Resolve(.,…): %v", err)
	}
	want := filepath.Join(root, "sub", "new", "file.txt")
	wantResolved, _ := filepath.EvalSymlinks(root)
	if wantResolved != "" {
		want = filepath.Join(wantResolved, "sub", "new", "file.txt")
	}
	if got != want {
		t.Errorf("Resolve(.,…) = %q, want %q", got, want)
	}
}

// TestRootRelForWrite_RelativeWorkdir is the same regression scoped
// to the call site fetch-binaries.go actually uses.
func TestRootRelForWrite_RelativeWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	gotRoot, gotRel, err := RootRelForWrite(".", filepath.Join("sub", "nested", "file.bin"))
	if err != nil {
		t.Fatalf("RootRelForWrite(.,…): %v", err)
	}
	if !filepath.IsAbs(gotRoot) {
		t.Errorf("expected absolute root, got %q", gotRoot)
	}
	if gotRel != filepath.Join("sub", "nested", "file.bin") {
		t.Errorf("rel = %q, want sub/nested/file.bin", gotRel)
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

func TestOpenReadFileRejectsSymlinkEscapeAtOpen(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	f, err := OpenReadFile(root, "link.txt")
	if f != nil {
		_ = f.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("OpenReadFile error = %v, want workdir escape", err)
	}
}

func TestReadRegularFileNoSymlinkRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := ReadRegularFileNoSymlinkLimited(filepath.Join(link, "secret.txt"), 1024)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ReadRegularFileNoSymlinkLimited error = %v, want symlink rejection", err)
	}
}

func TestReadRegularFileNoSymlinkRejectsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := ReadRegularFileNoSymlinkLimited(link, 1024)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ReadRegularFileNoSymlinkLimited error = %v, want symlink rejection", err)
	}
}

func TestReadRegularFileNoSymlinkLimitedReadsRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := ReadRegularFileNoSymlinkLimited(path, 2)
	if err != nil {
		t.Fatalf("ReadRegularFileNoSymlinkLimited: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("content = %q, want ok", data)
	}
}

func TestReadRegularFileNoSymlinkLimitedRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadRegularFileNoSymlinkLimited(path, 3)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadRegularFileNoSymlinkLimited error = %v, want size rejection", err)
	}
}

func TestReadRootRegularFileLimitedRejectsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	root, err := OpenRootNoSymlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	_, err = ReadRootRegularFileLimited(root, "link.txt", 1024)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ReadRootRegularFileLimited error = %v, want symlink rejection", err)
	}
}

func TestReadRootRegularFileLimitedRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.bin")
	if err := os.WriteFile(path, []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRootNoSymlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	_, err = ReadRootRegularFileLimited(root, "large.bin", 3)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadRootRegularFileLimited error = %v, want size rejection", err)
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

func TestMkdirAllNoSymlinkRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := MkdirAllNoSymlink(filepath.Join(link, "child"), 0o755)
	if err == nil {
		t.Fatal("MkdirAllNoSymlink should reject symlinked parent dirs")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "child")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}

func TestMkdirAllNoSymlinkCreatesNestedDirs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "c")
	if err := MkdirAllNoSymlink(path, 0o755); err != nil {
		t.Fatalf("MkdirAllNoSymlink: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a dir", path)
	}
}

func TestOpenRootNoSymlinkRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	root, err := OpenRootNoSymlink(filepath.Join(link, "child"))
	if err == nil {
		_ = root.Close()
		t.Fatal("OpenRootNoSymlink should reject symlinked parent dirs")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestRemoveAllNoSymlinkRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "victim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "victim", "keep.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := RemoveAllNoSymlink(filepath.Join(link, "victim"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("RemoveAllNoSymlink error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "victim", "keep.txt")); statErr != nil {
		t.Fatalf("symlink target was modified: %v", statErr)
	}
}

func TestRemoveAllNoSymlinkRejectsFinalSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := RemoveAllNoSymlink(link)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("RemoveAllNoSymlink error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "keep.txt")); statErr != nil {
		t.Fatalf("symlink target was modified: %v", statErr)
	}
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Fatalf("final symlink should remain after rejection: %v", statErr)
	}
}

func TestRemoveAllNoSymlinkRemovesDirectoryTree(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(filepath.Join(victim, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "nested", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAllNoSymlink(victim); err != nil {
		t.Fatalf("RemoveAllNoSymlink: %v", err)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Fatalf("victim should be removed, stat err = %v", err)
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

func TestWriteRootFileAtomicExactModeReplacesExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := WriteRootFileAtomicExactMode(root, "script.sh", []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("WriteRootFileAtomicExactMode: %v", err)
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

func TestGlobLimitedCountsTotalWithStoredCap(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	matches, total, err := GlobLimited(root, "*.txt", 2)
	if err != nil {
		t.Fatalf("GlobLimited: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(matches) != 2 {
		t.Fatalf("stored matches = %d, want 2: %v", len(matches), matches)
	}
}

func TestGlobLimitedRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := globLimited(root, "*", 10, globLimits{maxEntries: 1, maxDepth: maxGlobWalkDepth})
	if err == nil || !strings.Contains(err.Error(), "more than 1 entries") {
		t.Fatalf("globLimited error = %v, want entry cap", err)
	}
}

func TestGlobLimitedSkipsSymlinkDirectoryTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	matches, total, err := GlobLimited(root, "linkdir/*.txt", 10)
	if err != nil {
		t.Fatalf("GlobLimited: %v", err)
	}
	if total != 0 || len(matches) != 0 {
		t.Fatalf("symlink directory was traversed: total=%d matches=%v", total, matches)
	}
}

// Regression: ReadRegularFileUnderUserConfigLimited must succeed when the
// trust-anchor chain (HOME / XDG_*_HOME) traverses a symlink — that's the
// Atomic Fedora `/home → /var/home` shape that broke pre-v0.26.0 boot.
func TestReadRegularFileUnderUserConfigLimitedFollowsAnchorSymlink(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "var-home", "user")
	if err := os.MkdirAll(filepath.Join(realHome, "config", "stado"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realHome, "config", "stado", "system-prompt.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Operator-layout symlink: $base/home → var-home (the analogue of
	// /home → /var/home on Atomic).
	homeLink := filepath.Join(base, "home")
	if err := os.Symlink("var-home", homeLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	t.Setenv("HOME", filepath.Join(homeLink, "user"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeLink, "user", "config"))

	data, err := ReadRegularFileUnderUserConfigLimited(filepath.Join(homeLink, "user", "config", "stado", "system-prompt.md"), 1024)
	if err != nil {
		t.Fatalf("ReadRegularFileUnderUserConfigLimited: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want hello", data)
	}
}

// Defense-in-depth: a symlink BELOW the trust anchor must still be rejected
// — the relaxation only applies to operator-layout symlinks above the anchor.
func TestReadRegularFileUnderUserConfigLimitedRejectsInUserSymlink(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "config", "stado"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(target, []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "config", "stado", "system-prompt.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))

	_, err := ReadRegularFileUnderUserConfigLimited(link, 1024)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ReadRegularFileUnderUserConfigLimited error = %v, want symlink rejection", err)
	}
}
