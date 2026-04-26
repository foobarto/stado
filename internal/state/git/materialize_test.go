package git

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

func TestMaterialize_RoundTrip(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	wt := t.TempDir()
	sess, _ := CreateSession(sc, wt, "mat-1", plumbing.ZeroHash)

	// Populate + build tree from the source worktree.
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)
	os.WriteFile(filepath.Join(src, "exe"), []byte("#!/bin/sh\necho hi\n"), 0o755)

	tree, err := sess.BuildTreeFromDir(src)
	if err != nil {
		t.Fatalf("BuildTreeFromDir: %v", err)
	}

	// Materialise into a fresh dir and compare.
	dst := filepath.Join(t.TempDir(), "dst")
	if err := sess.MaterializeTreeToDir(tree, dst); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	for _, p := range []string{"a.txt", "sub/b.txt", "exe"} {
		srcBody, _ := os.ReadFile(filepath.Join(src, p))
		dstBody, err := os.ReadFile(filepath.Join(dst, p))
		if err != nil {
			t.Errorf("missing after materialise: %s (%v)", p, err)
			continue
		}
		if !bytes.Equal(srcBody, dstBody) {
			t.Errorf("body mismatch at %s", p)
		}
	}

	// Exec bit preserved?
	info, err := os.Stat(filepath.Join(dst, "exe"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("exe mode %o lost executable bit", info.Mode().Perm())
	}
}

func TestMaterializeRejectsDestinationRootSymlink(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-root-symlink", plumbing.ZeroHash)

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(src)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "dst-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := sess.MaterializeTreeToDir(tree, link); err == nil {
		t.Fatal("MaterializeTreeToDir should reject symlinked destination roots")
	}
	if _, err := os.Stat(filepath.Join(target, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified, stat err = %v", err)
	}
}

func TestMaterialize_Replacing_RemovesExtras(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	wt := t.TempDir()
	sess, _ := CreateSession(sc, wt, "mat-2", plumbing.ZeroHash)

	// Tree contains only a.txt.
	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	tree, _ := sess.BuildTreeFromDir(src)

	// Destination starts with a.txt + extra stuff.
	dst := filepath.Join(t.TempDir(), "dst")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "a.txt"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dst, "extra.txt"), []byte("kill me"), 0o644)
	os.MkdirAll(filepath.Join(dst, "stale"), 0o755)
	os.WriteFile(filepath.Join(dst, "stale", "x"), []byte("x"), 0o644)

	if err := sess.MaterializeTreeReplacing(tree, dst); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dst, "extra.txt")); err == nil {
		t.Error("extra.txt should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(dst, "stale")); err == nil {
		t.Error("stale/ should have been pruned")
	}
	body, _ := os.ReadFile(filepath.Join(dst, "a.txt"))
	if string(body) != "a" {
		t.Errorf("a.txt = %q, want updated content 'a'", body)
	}
}

func TestMaterialize_Replacing_PrunesStaleSymlinkWithoutTouchingTarget(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-prune-symlink", plumbing.ZeroHash)

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(src)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "stale-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := sess.MaterializeTreeReplacing(tree, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "stale-link")); !os.IsNotExist(err) {
		t.Fatalf("stale symlink should have been pruned, got %v", err)
	}
	body, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "outside" {
		t.Fatalf("prune removed through stale symlink target: %q", body)
	}
}

func TestMaterialize_Replacing_PreservesStadoInternal(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	wt := t.TempDir()
	sess, _ := CreateSession(sc, wt, "mat-3", plumbing.ZeroHash)

	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "keep"), []byte("k"), 0o644)
	tree, _ := sess.BuildTreeFromDir(src)

	dst := filepath.Join(t.TempDir(), "dst")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "keep"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dst, ".stado-pid"), []byte("12345"), 0o644)

	if err := sess.MaterializeTreeReplacing(tree, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".stado-pid")); err != nil {
		t.Errorf(".stado-pid should be preserved: %v", err)
	}
}

func TestMaterialize_RejectsEscapingTreeEntryName(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-escape", plumbing.ZeroHash)

	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob, err := sess.writeBlob(payload, false)
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	tree, err := sess.entriesToTree([]treeEntry{{
		name: "../escape.txt",
		hash: blob,
		mode: filemode.Regular,
	}})
	if err != nil {
		t.Fatalf("entriesToTree: %v", err)
	}

	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	if err := sess.MaterializeTreeToDir(tree, dst); err == nil {
		t.Fatal("MaterializeTreeToDir succeeded for escaping tree entry")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaping tree entry wrote outside destination: %v", err)
	}
}

func TestMaterialize_ReplacesDestinationFileSymlink(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-file-symlink", plumbing.ZeroHash)

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(src)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "a.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := sess.MaterializeTreeToDir(tree, dst); err != nil {
		t.Fatal(err)
	}
	outsideBody, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideBody) != "outside" {
		t.Fatalf("materialize wrote through destination file symlink: %q", outsideBody)
	}
	info, err := os.Lstat(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("destination symlink should be replaced by a regular file")
	}
	body, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "tree" {
		t.Fatalf("materialized file = %q, want tree", body)
	}
}

func TestMaterialize_ReplacesDestinationDirSymlink(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-dir-symlink", plumbing.ZeroHash)

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(src)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "b.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "sub")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := sess.MaterializeTreeToDir(tree, dst); err != nil {
		t.Fatal(err)
	}
	outsideBody, err := os.ReadFile(filepath.Join(outside, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideBody) != "outside" {
		t.Fatalf("materialize wrote through destination dir symlink: %q", outsideBody)
	}
	info, err := os.Lstat(filepath.Join(dst, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("destination symlink should be replaced by a directory, mode=%v", info.Mode())
	}
	body, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "tree" {
		t.Fatalf("materialized nested file = %q, want tree", body)
	}
}

func TestMaterialize_ZeroTreeEmptyDir(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-4", plumbing.ZeroHash)

	dst := filepath.Join(t.TempDir(), "dst")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "stuff"), []byte("x"), 0o644)

	// Non-replacing + zero tree → no-op (dst/stuff survives).
	if err := sess.MaterializeTreeToDir(plumbing.ZeroHash, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stuff")); err != nil {
		t.Error("non-replacing zero-tree should be a no-op")
	}

	// Replacing + zero tree → wipe dst content.
	if err := sess.MaterializeTreeReplacing(plumbing.ZeroHash, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stuff")); err == nil {
		t.Error("replacing zero-tree should have wiped content")
	}
}

func TestMaterialize_ZeroTreeWipesSymlinkWithoutTouchingTarget(t *testing.T) {
	sc, _ := OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	sess, _ := CreateSession(sc, t.TempDir(), "mat-zero-symlink", plumbing.ZeroHash)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "stale-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := sess.MaterializeTreeReplacing(plumbing.ZeroHash, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "stale-link")); !os.IsNotExist(err) {
		t.Fatalf("stale symlink should have been wiped, got %v", err)
	}
	body, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "outside" {
		t.Fatalf("zero-tree wipe removed through stale symlink target: %q", body)
	}
}
