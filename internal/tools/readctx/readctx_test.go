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
func (s stubHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (s stubHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

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

func TestRun_CapsRequestedMaxBytesPerFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sparse.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(maxReadctxFileBytes)+1024); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":               "sparse.txt",
		"max_bytes_per_file": maxReadctxFileBytes * 4,
	})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "[truncated]") {
		t.Errorf("truncation marker missing:\n%s", res.Content)
	}
	if len(res.Content) > maxReadctxFileBytes+512 {
		t.Errorf("response exceeds hard cap allowance: %d", len(res.Content))
	}
}

func TestRun_RejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": outside})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if err == nil {
		t.Fatal("expected workdir escape to fail")
	}
	if !strings.Contains(res.Error, "escapes workdir") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestRun_SkipsSymlinkedImportedPackageOutsideWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "util"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "util", "util.go"), []byte("package util\n\nconst Secret = \"nope\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "util"), filepath.Join(root, "util")); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}
	mainSrc := `package main

import "example.com/demo/util"

func main() { _ = util.Secret }
`
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"path": "main.go"})
	res, err := (Tool{}).Run(context.Background(), args, stubHost{wd: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Content, "=== util/util.go ===") || strings.Contains(res.Content, "const Secret") {
		t.Fatalf("symlinked outside import should not be included:\n%s", res.Content)
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x.y/z\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	gotRoot, gotPath := findModuleRoot(filepath.Join(root, "a", "b"), root)
	if gotRoot != root {
		t.Errorf("module root = %q, want %q", gotRoot, root)
	}
	if gotPath != "x.y/z" {
		t.Errorf("module path = %q", gotPath)
	}
}

func TestFindModuleRoot_None(t *testing.T) {
	root := t.TempDir()
	gotRoot, gotPath := findModuleRoot(root, root)
	if gotRoot != "" || gotPath != "" {
		t.Errorf("expected empty results, got (%q,%q)", gotRoot, gotPath)
	}
}

func TestFindModuleRoot_StopsAtWorkdir(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "go.mod"), []byte("module outside.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(parent, "work")
	nested := filepath.Join(workdir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	gotRoot, gotPath := findModuleRoot(nested, workdir)
	if gotRoot != "" || gotPath != "" {
		t.Fatalf("module root crossed workdir boundary: (%q,%q)", gotRoot, gotPath)
	}
}
