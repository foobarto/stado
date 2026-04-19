package lspfind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

type stubHost struct{ wd string }

func (s stubHost) Approve(ctx context.Context, req tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (s stubHost) Workdir() string { return s.wd }

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
	args, _ := json.Marshal(map[string]any{"path": "foo.md", "line": 1, "column": 1})
	res, err := (&FindDefinition{}).Run(context.Background(), args, stubHost{wd: "/tmp"})
	if err == nil {
		t.Error("expected error for unmapped extension")
	}
	if !strings.Contains(res.Error, "no LSP server") {
		t.Errorf("error = %q", res.Error)
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
		".go":   "go",
		".py":   "python",
		".tsx":  "typescriptreact",
		".jsx":  "javascriptreact",
		".rs":   "rust",
		".md":   "plaintext",
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
