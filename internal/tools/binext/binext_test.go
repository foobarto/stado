package binext

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestExtract_NotBundled(t *testing.T) {
	_, err := Extract(t.TempDir(), "rg", nil, "")
	if !errors.Is(err, ErrNotBundled) {
		t.Errorf("nil bundled should return ErrNotBundled, got %v", err)
	}
	_, err = Extract(t.TempDir(), "rg", []byte{}, "")
	if !errors.Is(err, ErrNotBundled) {
		t.Errorf("empty bundled should return ErrNotBundled, got %v", err)
	}
}

func TestExtract_HappyPathWritesExecutable(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("#!/bin/sh\necho hello\n")
	path, err := Extract(dir, "rg", blob, shaOf(blob))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Errorf("extracted path %q not in cache dir %q", path, dir)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("extracted binary not executable: %v", info.Mode())
	}
	// Content matches.
	got, _ := os.ReadFile(path)
	if string(got) != string(blob) {
		t.Errorf("content mismatch")
	}
}

func TestExtract_RejectsPathLikeToolName(t *testing.T) {
	_, err := Extract(t.TempDir(), "../tool", []byte("bytes"), "")
	if err == nil {
		t.Fatal("expected path-like tool name to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid tool name") {
		t.Fatalf("expected invalid tool name error, got %v", err)
	}
}

func TestExtract_DoesNotFollowPredictableTempSymlink(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("binary-bytes")
	suffix := shaOf(blob)[:12]
	path := filepath.Join(dir, "tool-"+suffix)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path+".tmp"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	gotPath, err := Extract(dir, "tool", blob, shaOf(blob))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	target, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != "outside" {
		t.Fatalf("outside target was modified: %q", target)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(blob) {
		t.Fatalf("cache content = %q, want %q", got, blob)
	}
}

func TestExtract_ReplacesExistingCacheSymlink(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("binary-bytes")
	path := filepath.Join(dir, "tool-"+shaOf(blob)[:12])
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, blob, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := Extract(dir, "tool", blob, shaOf(blob)); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cache path is still a symlink: %v", info.Mode())
	}
}

func TestExtractRejectsCacheDirParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "cache-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	blob := []byte("binary-bytes")

	if _, err := Extract(link, "tool", blob, shaOf(blob)); err == nil {
		t.Fatal("Extract should reject symlinked cache dirs")
	}
	if entries, err := os.ReadDir(target); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("symlink target was modified with %d entries", len(entries))
	}
}

func TestExtract_CacheHitSkipsRewrite(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("binary-bytes")
	path, _ := Extract(dir, "tool", blob, shaOf(blob))

	// Touch mtime so we can detect a rewrite.
	info1, _ := os.Stat(path)
	path2, err := Extract(dir, "tool", blob, shaOf(blob))
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if path != path2 {
		t.Errorf("cache hit produced different path: %q vs %q", path, path2)
	}
	info2, _ := os.Stat(path2)
	if info1.ModTime() != info2.ModTime() {
		t.Errorf("cache hit rewrote the file (mtime changed)")
	}
}

func TestExtract_RewritesWrongSizedCacheEntry(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("binary-bytes")
	path := filepath.Join(dir, "tool-"+shaOf(blob)[:12])
	if err := os.WriteFile(path, append(blob, 'x'), 0o700); err != nil {
		t.Fatal(err)
	}

	gotPath, err := Extract(dir, "tool", blob, shaOf(blob))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(blob) {
		t.Fatalf("cache entry was not rewritten: %q", got)
	}
}

func TestExtract_DigestMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("one thing")
	_, err := Extract(dir, "tool", blob, shaOf([]byte("different thing")))
	if err == nil {
		t.Fatal("expected digest-mismatch error")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("error doesn't call out the mismatch: %v", err)
	}
}

func TestExtract_EmptyExpectedSHASkipsCheck(t *testing.T) {
	// Release CI computes + pins the sha. Dev builds may ship without
	// a pinned digest; empty expectedSHA means "don't verify, just
	// extract". Documented in Extract's godoc.
	dir := t.TempDir()
	blob := []byte("trust-on-first-use")
	path, err := Extract(dir, "tool", blob, "")
	if err != nil {
		t.Fatalf("Extract with empty digest: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("path missing: %v", err)
	}
}

func TestExtract_DivergentBlobTriggersNewCacheFile(t *testing.T) {
	dir := t.TempDir()
	a := []byte("version-a")
	b := []byte("version-b-different")
	pathA, _ := Extract(dir, "tool", a, "")
	pathB, _ := Extract(dir, "tool", b, "")
	if pathA == pathB {
		t.Errorf("different content should produce different cache paths (got %q == %q)", pathA, pathB)
	}
	// Both should still exist.
	for _, p := range []string{pathA, pathB} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("cached file %q missing: %v", p, err)
		}
	}
}

func TestExtract_FailsWhenCacheDirUnwritable(t *testing.T) {
	// Point at a path that can't be created — an existing file
	// blocks MkdirAll.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Extract(blocker, "tool", []byte("bytes"), "")
	if err == nil {
		t.Fatal("expected cache-dir creation to fail")
	}
}
