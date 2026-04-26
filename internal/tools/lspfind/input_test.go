package lspfind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLSPDocumentTextSmallFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readLSPDocumentText(root, "small.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "package main\n" {
		t.Fatalf("document text = %q", got)
	}
}

func TestReadLSPDocumentTextRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "huge.go")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxLSPDocumentBytes+1); err != nil {
		t.Fatal(err)
	}
	_, err := readLSPDocumentText(root, "huge.go")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readLSPDocumentText error = %v, want size rejection", err)
	}
}
