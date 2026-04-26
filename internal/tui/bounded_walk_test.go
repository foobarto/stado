package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkTUIRepoRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", i+1)+".txt"), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	err := walkTUIRepo(root, 2, maxTUIRepoScanDepth, func(string, os.FileInfo) tuiRepoWalkDecision {
		return tuiRepoWalkContinue
	})
	if err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Fatalf("walkTUIRepo error = %v, want entry cap rejection", err)
	}
}

func TestWalkTUIRepoRejectsTooDeepTree(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "note.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := walkTUIRepo(root, 10, 1, func(string, os.FileInfo) tuiRepoWalkDecision {
		return tuiRepoWalkContinue
	})
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds 1") {
		t.Fatalf("walkTUIRepo error = %v, want depth cap rejection", err)
	}
}
