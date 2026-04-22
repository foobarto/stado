package rg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

func haveRipgrep(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed; skipping integration test")
	}
	return path
}

func TestResolveBinary_Override(t *testing.T) {
	b, err := ResolveBinary("/custom/rg")
	if err != nil || b != "/custom/rg" {
		t.Errorf("override ignored: %q %v", b, err)
	}
}

func TestResolveBinary_EnvVar(t *testing.T) {
	t.Setenv("STADO_RG", "/env/rg")
	b, err := ResolveBinary("")
	if err != nil || b != "/env/rg" {
		t.Errorf("env ignored: %q %v", b, err)
	}
}

func TestResolveBinary_NoRG_ErrorMessage(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("STADO_RG", "")
	_, err := ResolveBinary("")
	if err == nil {
		t.Fatal("expected error when rg missing")
	}
	if !strings.Contains(err.Error(), "ripgrep not found") {
		t.Errorf("error lacks install hint: %v", err)
	}
}

func TestSchema_RequiresPattern(t *testing.T) {
	s := Tool{}.Schema()
	req, _ := s["required"].([]string)
	if len(req) != 1 || req[0] != "pattern" {
		t.Errorf("required = %v, want [pattern]", req)
	}
}

func TestRun_Integration_MatchesFound(t *testing.T) {
	haveRipgrep(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package main\nfunc main() { log.Printf(\"hello\") }\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package main\nfunc other() {}\n")

	args, _ := json.Marshal(map[string]any{"pattern": "main"})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	// Should report two matches — "a.go" and "b.go" both contain `main`.
	lines := strings.Count(res.Content, "\n") + 1
	if lines < 2 {
		t.Errorf("got %d result lines, want ≥2\n%s", lines, res.Content)
	}
	if !strings.Contains(res.Content, "a.go:1:") {
		t.Errorf("output missing a.go line-1 match:\n%s", res.Content)
	}
}

func TestRun_Integration_NoMatches(t *testing.T) {
	haveRipgrep(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package foo\n")
	args, _ := json.Marshal(map[string]any{"pattern": "nothing-here-xyzzy"})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "No matches found" {
		t.Errorf("content = %q, want 'No matches found'", res.Content)
	}
}

func TestRun_Integration_GlobFilter(t *testing.T) {
	haveRipgrep(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "needle\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "needle\n")

	args, _ := json.Marshal(map[string]any{"pattern": "needle", "globs": []string{"*.go"}})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "b.txt") {
		t.Errorf("glob filter should exclude b.txt:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("glob filter should include a.go:\n%s", res.Content)
	}
}

func TestRun_RejectsEmptyPattern(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"pattern": ""})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: "/tmp"})
	if err == nil {
		t.Error("expected error on empty pattern")
	}
	if !strings.Contains(res.Error, "pattern required") {
		t.Errorf("error wrong: %q", res.Error)
	}
}

func TestRun_RejectsEscapingPath(t *testing.T) {
	haveRipgrep(t)
	args, _ := json.Marshal(map[string]any{"pattern": "main", "path": "../"})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: t.TempDir()})
	if err == nil {
		t.Fatal("expected workdir escape to fail")
	}
	if !strings.Contains(res.Error, "escapes workdir") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestClass_IsNonMutating(t *testing.T) {
	if (Tool{}).Class() != tool.ClassNonMutating {
		t.Error("ripgrep should be non-mutating")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
