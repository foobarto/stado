package astgrep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestResolveBinary_Override(t *testing.T) {
	if b, err := ResolveBinary("/custom/sg"); err != nil || b != "/custom/sg" {
		t.Errorf("override: %q %v", b, err)
	}
}

func TestResolveBinary_EnvVar(t *testing.T) {
	t.Setenv("STADO_AST_GREP", "/env/sg")
	if b, err := ResolveBinary(""); err != nil || b != "/env/sg" {
		t.Errorf("env: %q %v", b, err)
	}
}

func TestResolveBinary_NotFound_HasInstallHint(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("STADO_AST_GREP", "")
	_, err := ResolveBinary("")
	if err == nil {
		t.Fatal("expected error when ast-grep missing")
	}
	if !strings.Contains(err.Error(), "ast-grep not found") {
		t.Errorf("error wrong: %v", err)
	}
	if !strings.Contains(err.Error(), "brew install ast-grep") {
		t.Errorf("missing install hint: %v", err)
	}
}

func TestSchema_RequiresPattern(t *testing.T) {
	s := Tool{}.Schema()
	req, _ := s["required"].([]string)
	if len(req) != 1 || req[0] != "pattern" {
		t.Errorf("required = %v", req)
	}
}

func TestRun_RejectsEmptyPattern(t *testing.T) {
	args, _ := json.Marshal(map[string]any{})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: "/tmp"})
	if err == nil {
		t.Error("expected error on empty pattern")
	}
	if !strings.Contains(res.Error, "pattern required") {
		t.Errorf("res.Error = %q", res.Error)
	}
}

func TestRun_RejectsEscapingPath(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"pattern": "foo($X)", "path": "../"})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: t.TempDir()})
	if err == nil {
		t.Fatal("expected workdir escape to fail")
	}
	if !strings.Contains(res.Error, "escapes workdir") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestRun_RejectsOversizedOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ast-grep")
	body := fmt.Sprintf("#!/bin/sh\nyes x | head -c %d\n", maxASTGrepOutputBytes+1)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"pattern": "x"})
	res, err := (Tool{Binary: script}).Run(context.Background(), args, stubHost{wd: dir})
	if err == nil {
		t.Fatal("expected oversized output error")
	}
	if !strings.Contains(res.Error, "exceeds") {
		t.Fatalf("error = %q, want size rejection", res.Error)
	}
}

func TestClass_IsExec(t *testing.T) {
	if (Tool{}).Class() != tool.ClassExec {
		t.Error("ast_grep default class should be exec")
	}
}

func TestParseMatches_EmptyInput(t *testing.T) {
	got, err := parseMatches([]byte("   \n"), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty input → expected 0 matches, got %v", got)
	}
}

func TestParseMatches_FormatsLines(t *testing.T) {
	raw := []byte(`[{"file":"/tmp/a.go","range":{"start":{"line":3},"end":{"line":3}},"text":"fmt.Println(\"hi\")"}]`)
	got, err := parseMatches(raw, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 match, got %d", len(got))
	}
	if !strings.HasPrefix(got[0], "a.go:3-3:") {
		t.Errorf("format = %q", got[0])
	}
}

func TestParseMatches_TruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 200)
	raw, _ := json.Marshal([]map[string]any{{
		"file":  "/tmp/a.go",
		"range": map[string]any{"start": map[string]any{"line": 1}, "end": map[string]any{"line": 1}},
		"text":  long,
	}})
	got, _ := parseMatches(raw, "/tmp")
	if len(got) != 1 {
		t.Fatal("expected 1 match")
	}
	if !strings.HasSuffix(got[0], "…") {
		t.Errorf("long text should be truncated with ellipsis: %q", got[0])
	}
}
