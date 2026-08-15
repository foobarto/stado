package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// This file pins the END-TO-END security property that a symlink living
// inside a plugin's workdir cannot let the plugin's wasm FS imports
// write (or read) outside its declared fs:write / fs:read grant.
//
// The exploit shape it guards against: a plugin is granted
// fs:write:<workdir> only, but the workdir contains a directory symlink
// `link` -> some OUTSIDE directory. If the FS layer naively joined the
// workdir-relative path lexically and prefix-checked the *unresolved*
// string, then `stado_fs_write("link/x", ...)` would lexically look
// like it stays under <workdir> while actually landing OUTSIDE.
//
// The defense has two cooperating layers, each pinned below:
//  1. realPathForWrite / realPath resolve symlink components BEFORE the
//     grant check runs, so the grant check sees the OUTSIDE real path.
//  2. allowWrite / allowRead (pathAllowedExpanded) then reject that
//     resolved OUTSIDE path because it is not under any fs:write entry.
//  3. writeAllowedFile / openAllowedRoot (os.OpenRoot-based) are a
//     second, independent backstop: even called directly with an abs
//     path outside the allow set, or a ".."-traversal rel, they refuse.
//
// Helpers and test names are prefixed `fsSymEsc` to avoid identifier
// clashes with host_test.go (which already covers adjacent, but
// distinct, single-file-symlink cases).

// fsSymEscEvalAbs evaluates symlinks on a path and fails the test on
// error. Both t.TempDir() roots and the workdir may sit under a
// platform symlink (for example Fedora Atomic
// /home -> /var/home); the security property is about *resolved* real
// paths, so all comparisons must be made against the EvalSymlinks form.
func fsSymEscEvalAbs(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return filepath.Clean(resolved)
}

// fsSymEscUnder reports whether child equals parent or lies strictly
// under it (parent treated as a directory prefix).
func fsSymEscUnder(child, parent string) bool {
	parent = strings.TrimRight(filepath.Clean(parent), string(filepath.Separator))
	child = filepath.Clean(child)
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

// fsSymEscNewHost builds a Host with the given workdir and an fs:write
// grant scoped to exactly that workdir (no outside grant), matching how
// the runtime constructs the capability-gated bridge in production.
func fsSymEscNewHost(t *testing.T, workdir string) *Host {
	t.Helper()
	h := NewHost(plugins.Manifest{Name: "fssymesc-plugin"}, workdir, nil)
	// Grant write (and read) only to the workdir. realPath-resolved
	// targets are compared against these; a symlinked escape must miss.
	h.FSWrite = []string{workdir}
	h.FSRead = []string{workdir}
	return h
}

// TestFSSymEsc_WorkdirDirSymlinkResolvesAndIsDenied is the core
// end-to-end assertion. A directory symlink `link` inside the workdir
// points at an OUTSIDE directory. We prove:
//
//	(1) realPathForWrite(workdir, "link/x") resolves THROUGH the symlink
//	    to the OUTSIDE real path (it is NOT lexically left under workdir);
//	(2) the resulting absolute path is under the outside target, not the
//	    workdir;
//	(3) allowWrite(resolved) returns FALSE for a Host granted only the
//	    workdir — the grant check rejects the escape once resolved;
//	(4) the same holds for the read path (realPath + allowRead).
func TestFSSymEsc_WorkdirDirSymlinkResolvesAndIsDenied(t *testing.T) {
	// Two independent temp roots: the plugin's workdir, and an outside
	// directory the plugin must never be able to reach.
	workdir := fsSymEscEvalAbs(t, t.TempDir())
	outside := fsSymEscEvalAbs(t, t.TempDir())

	// A directory symlink inside the workdir that points OUTSIDE.
	link := filepath.Join(workdir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	// --- write path -------------------------------------------------
	// "link/x" is workdir-relative and lexically looks like it stays
	// under the workdir. realPathForWrite must resolve the `link`
	// component and land under `outside`.
	resolvedWrite, err := realPathForWrite(workdir, "link/x")
	if err != nil {
		t.Fatalf("realPathForWrite(workdir, %q): %v", "link/x", err)
	}
	wantWrite := filepath.Join(outside, "x")
	if resolvedWrite != wantWrite {
		t.Fatalf("realPathForWrite resolved to %q, want OUTSIDE real path %q", resolvedWrite, wantWrite)
	}
	// Property (2): the resolved path is under `outside`, NOT under the
	// workdir. If it were still lexically under workdir, the grant check
	// would wrongly pass.
	if !fsSymEscUnder(resolvedWrite, outside) {
		t.Fatalf("resolved write path %q is not under outside target %q (symlink not resolved before grant check)", resolvedWrite, outside)
	}
	if fsSymEscUnder(resolvedWrite, workdir) {
		t.Fatalf("resolved write path %q is still lexically under workdir %q — symlink escape would bypass the grant check", resolvedWrite, workdir)
	}

	// Property (3): grant check rejects the resolved escape.
	h := fsSymEscNewHost(t, workdir)
	if h.allowWrite(resolvedWrite) {
		t.Errorf("allowWrite(%q) = true; a workdir symlink resolving OUTSIDE the fs:write grant must be denied", resolvedWrite)
	}
	// Control: a genuinely-in-workdir target is still allowed.
	if !h.allowWrite(filepath.Join(workdir, "ok.txt")) {
		t.Errorf("allowWrite(<workdir>/ok.txt) = false; an in-grant write must be permitted (control)")
	}

	// --- read path --------------------------------------------------
	// Place a real file at the symlink target so realPath (which
	// EvalSymlinks the whole existing path) has something to resolve.
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedRead, err := realPath(workdir, "link/secret.txt")
	if err != nil {
		t.Fatalf("realPath(workdir, %q): %v", "link/secret.txt", err)
	}
	if resolvedRead != secret {
		t.Fatalf("realPath resolved to %q, want OUTSIDE real path %q", resolvedRead, secret)
	}
	if !fsSymEscUnder(resolvedRead, outside) || fsSymEscUnder(resolvedRead, workdir) {
		t.Fatalf("resolved read path %q escaped detection (outside=%q workdir=%q)", resolvedRead, outside, workdir)
	}
	if h.allowRead(resolvedRead) {
		t.Errorf("allowRead(%q) = true; a workdir symlink resolving OUTSIDE the fs:read grant must be denied", resolvedRead)
	}
}

// TestFSSymEsc_WriteAllowedFileRejectsOutsideAbs pins the
// writeAllowedFile / openAllowedRoot backstop: even handed an absolute
// path OUTSIDE the allow set directly (i.e. as if an upstream resolve
// failed open), it must refuse with os.ErrPermission and create no file.
func TestFSSymEsc_WriteAllowedFileRejectsOutsideAbs(t *testing.T) {
	allowed := fsSymEscEvalAbs(t, t.TempDir())
	outside := fsSymEscEvalAbs(t, t.TempDir())

	target := filepath.Join(outside, "pwned.txt")
	err := writeAllowedFile(target, []string{allowed}, []byte("pwned"), 0o644)
	if err == nil {
		t.Fatal("writeAllowedFile must reject an abs path outside the allow set")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("writeAllowedFile err = %v, want os.ErrPermission for out-of-allow path", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("out-of-allow write created a file at %q (stat err = %v)", target, statErr)
	}
}

// TestFSSymEsc_OpenAllowedRootRejectsOutsideAbs pins the same backstop
// at the openAllowedRoot level for both read and write modes.
func TestFSSymEsc_OpenAllowedRootRejectsOutsideAbs(t *testing.T) {
	allowed := fsSymEscEvalAbs(t, t.TempDir())
	outside := fsSymEscEvalAbs(t, t.TempDir())

	for _, allowMissing := range []bool{false, true} {
		root, rel, err := openAllowedRoot(filepath.Join(outside, "x"), []string{allowed}, allowMissing)
		if err == nil {
			_ = root.Close()
			t.Fatalf("openAllowedRoot(outside, allowMissing=%v) succeeded (rel=%q); want denial", allowMissing, rel)
		}
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("openAllowedRoot(allowMissing=%v) err = %v, want os.ErrPermission", allowMissing, err)
		}
		if root != nil {
			_ = root.Close()
			t.Fatalf("openAllowedRoot returned non-nil root alongside error (allowMissing=%v)", allowMissing)
		}
	}
}

// TestFSSymEsc_WriteAllowedFileRejectsTraversalRel pins that a path
// whose Rel-to-allowed-root is a ".." traversal is rejected by
// openAllowedRoot's containment guard, even though the parent of the
// allow root exists. We construct a sibling directory adjacent to the
// allow root and target a file inside it; relative to the allow root
// that target is "../sibling/escape.txt".
func TestFSSymEsc_WriteAllowedFileRejectsTraversalRel(t *testing.T) {
	base := fsSymEscEvalAbs(t, t.TempDir())
	allowed := filepath.Join(base, "allowed")
	sibling := filepath.Join(base, "sibling")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	// Absolute target is a clean path (no ".." remaining after Clean),
	// but it is NOT under `allowed` — openAllowedRoot's pathAllowed
	// prefix check fails first, yielding ErrPermission. This is the
	// "escape via lexical sibling" case.
	target := filepath.Join(sibling, "escape.txt")
	err := writeAllowedFile(target, []string{allowed}, []byte("pwned"), 0o644)
	if err == nil {
		t.Fatal("writeAllowedFile must reject a sibling-directory escape")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("writeAllowedFile err = %v, want os.ErrPermission", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("sibling escape created a file at %q (stat err = %v)", target, statErr)
	}

	// Control: a write genuinely WITHIN the allow set succeeds and is
	// readable back, proving the rejections above are not vacuous.
	good := filepath.Join(allowed, "nested", "ok.txt")
	if err := writeAllowedFile(good, []string{allowed}, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writeAllowedFile within allow set failed (control): %v", err)
	}
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatalf("reading back in-allow write: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("in-allow write content = %q, want %q", data, "hello")
	}
}
