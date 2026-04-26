package filepicker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkRepoFilesRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", i+1)+".txt"), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	err := walkRepoFiles(root, 2, maxRepoFileScanDepth, func(string, os.FileInfo) repoFileWalkDecision {
		return repoFileWalkContinue
	})
	if err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Fatalf("walkRepoFiles error = %v, want entry cap rejection", err)
	}
}

func TestWalkRepoFilesRejectsTooDeepTree(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "note.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := walkRepoFiles(root, 10, 1, func(string, os.FileInfo) repoFileWalkDecision {
		return repoFileWalkContinue
	})
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds 1") {
		t.Fatalf("walkRepoFiles error = %v, want depth cap rejection", err)
	}
}
