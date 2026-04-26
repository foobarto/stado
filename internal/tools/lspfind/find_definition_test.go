package lspfind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/pkg/tool"
)

type stubHost struct{ wd string }

func (s stubHost) Approve(ctx context.Context, req tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (s stubHost) Workdir() string { return s.wd }
func (s stubHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (s stubHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

func TestSchema_RequiresAllCoordinates(t *testing.T) {
	s := (&FindDefinition{}).Schema()
	req := s["required"].([]string)
	if len(req) != 3 {
		t.Fatalf("required fields = %v", req)
	}
}

func TestRun_RejectsMissingArgs(t *testing.T) {
	cases := []map[string]any{
		{},
		{"path": "foo.go"},
		{"path": "foo.go", "line": 1},
		{"path": "foo.go", "line": 0, "column": 1},
	}
	for i, c := range cases {
		args, _ := json.Marshal(c)
		res, err := (&FindDefinition{}).Run(context.Background(), args, stubHost{wd: "/tmp"})
		if err == nil {
			t.Errorf("case %d: expected error, got none", i)
		}
		if !strings.Contains(res.Error, "required") {
			t.Errorf("case %d: error wrong: %q", i, res.Error)
		}
	}
}

func TestRun_UnknownExtension(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "foo.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": "foo.md", "line": 1, "column": 1})
	res, err := (&FindDefinition{}).Run(context.Background(), args, stubHost{wd: root})
	if err == nil {
		t.Error("expected error for unmapped extension")
	}
	if !strings.Contains(res.Error, "no LSP server") {
		t.Errorf("error = %q", res.Error)
	}
}

func TestRun_RejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "foo.go")
	if err := os.WriteFile(outside, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": outside, "line": 1, "column": 1})
	res, err := (&FindDefinition{}).Run(context.Background(), args, stubHost{wd: root})
	if err == nil {
		t.Fatal("expected workdir escape to fail")
	}
	if !strings.Contains(res.Error, "escapes workdir") {
		t.Fatalf("unexpected error: %q", res.Error)
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

func TestClass(t *testing.T) {
	if (&FindDefinition{}).Class() != tool.ClassNonMutating {
		t.Error("find_definition should be non-mutating")
	}
}
