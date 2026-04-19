package readctx

import (
	"context"
	"encoding/json"
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

// setupGoModule creates a minimal Go module layout with a main file that
// imports a local subpackage. Returns (workdir, main-file-relpath).
func setupGoModule(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "util"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "util", "util.go"), []byte("package util\n\nfunc Helper() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainSrc := `package main

import (
	"fmt"
	"example.com/demo/util"
)

func main() { fmt.Println(util.Helper()) }
`
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, "main.go"
}

func TestRun_RequiresPath(t *testing.T) {
	res, err := (Tool{}).Run(context.Background(), json.RawMessage(`{}`), stubHost{wd: "/tmp"})
	if err == nil {
		t.Error("expected error when path missing")
	}
	if !strings.Contains(res.Error, "path required") {
		t.Errorf("res.Error = %q", res.Error)
	}
}

func TestRun_IncludesDirectGoImports(t *testing.T) {
	root, entry := setupGoModule(t)
	args, _ := json.Marshal(map[string]any{"path": entry})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Content, "=== main.go ===") {
		t.Errorf("main.go heading missing:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "=== util/util.go ===") {
		t.Errorf("imported util file not included:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "Helper()") {
		t.Errorf("imported file body missing Helper():\n%s", res.Content)
	}
}

func TestRun_SkipsExternalImports(t *testing.T) {
	root, entry := setupGoModule(t)
	args, _ := json.Marshal(map[string]any{"path": entry})
	res, _ := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	// fmt is a stdlib import — must not be read.
	if strings.Contains(res.Content, "Package fmt implements") {
		t.Errorf("stdlib content should not appear:\n%s", res.Content)
	}
}

func TestRun_HandlesNonGoFilesAsPlainText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": "note.txt"})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("note.txt content missing:\n%s", res.Content)
	}
	if strings.Count(res.Content, "=== ") != 1 {
		t.Errorf("expected exactly one file heading, got:\n%s", res.Content)
	}
}

func TestRun_RejectsDirectory(t *testing.T) {
	root := t.TempDir()
	args, _ := json.Marshal(map[string]any{"path": "."})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if err == nil {
		t.Error("expected error for directory path")
	}
	if !strings.Contains(res.Error, "directory") {
		t.Errorf("res.Error = %q", res.Error)
	}
}

func TestRun_TruncatesLargeFiles(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", 1000)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": "big.txt", "max_bytes_per_file": 100})
	res, _ := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if !strings.Contains(res.Content, "[truncated]") {
		t.Errorf("truncation marker missing:\n%s", res.Content)
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x.y/z\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	gotRoot, gotPath := findModuleRoot(filepath.Join(root, "a", "b"))
	if gotRoot != root {
		t.Errorf("module root = %q, want %q", gotRoot, root)
	}
	if gotPath != "x.y/z" {
		t.Errorf("module path = %q", gotPath)
	}
}

func TestFindModuleRoot_None(t *testing.T) {
	root := t.TempDir()
	gotRoot, gotPath := findModuleRoot(root)
	if gotRoot != "" || gotPath != "" {
		t.Errorf("expected empty results, got (%q,%q)", gotRoot, gotPath)
	}
}
