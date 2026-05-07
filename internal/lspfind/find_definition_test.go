package lspfind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/lsp"
)

func TestFindDefinition_RejectsMissingArgs(t *testing.T) {
	cases := []Args{
		{},
		{Path: "foo.go"},
		{Path: "foo.go", Line: 1},
		{Path: "foo.go", Line: 0, Column: 1},
	}
	for i, c := range cases {
		_, err := FindDefinition(context.Background(), c, "/tmp")
		if err == nil {
			t.Errorf("case %d: expected error, got none", i)
			continue
		}
		if !strings.Contains(err.Error(), "required") {
			t.Errorf("case %d: error wrong: %q", i, err)
		}
	}
}

func TestFindDefinition_UnknownExtension(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "foo.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := FindDefinition(context.Background(),
		Args{Path: "foo.md", Line: 1, Column: 1}, root)
	if err == nil {
		t.Fatal("expected error for unmapped extension")
	}
	if !strings.Contains(err.Error(), "no LSP server") {
		t.Errorf("error = %q", err)
	}
}

func TestFindDefinition_RejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "foo.go")
	if err := os.WriteFile(outside, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := FindDefinition(context.Background(),
		Args{Path: outside, Line: 1, Column: 1}, root)
	if err == nil {
		t.Fatal("expected workdir escape to fail")
	}
	if !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("unexpected error: %q", err)
	}
}

func TestFormatWorkdirLocationsFiltersEscapes(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "main.go")
	outside := filepath.Join(t.TempDir(), "secret.go")
	if err := os.WriteFile(inside, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("package secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := formatWorkdirLocations(root, []lsp.Location{
		{
			URI: "file://" + outside,
			Range: lsp.Range{
				Start: lsp.Position{Line: 0, Character: 0},
			},
		},
		{
			URI: "file://" + inside,
			Range: lsp.Range{
				Start: lsp.Position{Line: 2, Character: 4},
			},
		},
	})
	if strings.Contains(got, "secret.go") || strings.Contains(got, "..") {
		t.Fatalf("escaped LSP location leaked into output: %q", got)
	}
	if strings.TrimSpace(got) != "main.go:3:5" {
		t.Fatalf("filtered locations = %q, want main.go:3:5", got)
	}
}

func TestFormatWorkdirLocationsRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.go")
	if err := os.WriteFile(outside, []byte("package secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	got := formatWorkdirLocations(root, []lsp.Location{{
		URI: "file://" + link,
		Range: lsp.Range{
			Start: lsp.Position{Line: 0, Character: 0},
		},
	}})
	if got != "" {
		t.Fatalf("symlink escape LSP location should be filtered, got %q", got)
	}
}

func TestServerFor_KnownExtensions(t *testing.T) {
	cases := map[string]string{
		".go":  "gopls",
		".rs":  "rust-analyzer",
		".py":  "pyright",
		".ts":  "typescript-language-server",
		".tsx": "typescript-language-server",
		".md":  "",
	}
	for ext, want := range cases {
		if got := serverFor(ext); got != want {
			t.Errorf("serverFor(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestLanguageIDFor(t *testing.T) {
	cases := map[string]string{
		".go":  "go",
		".py":  "python",
		".tsx": "typescriptreact",
		".jsx": "javascriptreact",
		".rs":  "rust",
		".md":  "plaintext",
	}
	for ext, want := range cases {
		if got := languageIDFor(ext); got != want {
			t.Errorf("languageIDFor(%q) = %q", ext, got)
		}
	}
}
