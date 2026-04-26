package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

// TestReadTruncatesLargeFile asserts the read tool caps at
// budget.ReadBytes and emits the DESIGN-spec'd marker.
func TestReadTruncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x\n", budget.ReadBytes) // ~2× the cap
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h := newRecordingHost(dir)

	raw, _ := json.Marshal(map[string]any{"path": "big.txt"})
	res, err := ReadTool{}.Run(context.Background(), raw, h)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Content) > budget.ReadBytes+256 {
		t.Errorf("result exceeds budget: %d > %d", len(res.Content), budget.ReadBytes+256)
	}
	if !strings.Contains(res.Content, "[truncated:") {
		t.Errorf("truncation marker missing: tail=%q", res.Content[max0(len(res.Content)-200):])
	}
	if !strings.Contains(res.Content, "call big.txt with start=") {
		t.Errorf("hint missing — model should know how to page: tail=%q", res.Content[max0(len(res.Content)-200):])
	}
}

func TestReadSparseLargeFileIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.bin")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Truncate(path, int64(budget.ReadBytes)*1024); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	h := newRecordingHost(dir)

	raw, _ := json.Marshal(map[string]any{"path": "sparse.bin"})
	res, err := ReadTool{}.Run(context.Background(), raw, h)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Content) > budget.ReadBytes+256 {
		t.Errorf("result exceeds budget: %d > %d", len(res.Content), budget.ReadBytes+256)
	}
	if !strings.Contains(res.Content, "[truncated:") {
		t.Errorf("truncation marker missing for sparse file")
	}
}

// TestReadNoTruncationUnderBudget is the negative: small files pass
// through unchanged.
func TestReadNoTruncationUnderBudget(t *testing.T) {
	dir := t.TempDir()
	body := "tiny\n"
	writeTempFile(t, dir, "small.txt", body)
	h := newRecordingHost(dir)

	raw, _ := json.Marshal(map[string]any{"path": "small.txt"})
	res, _ := ReadTool{}.Run(context.Background(), raw, h)
	if res.Content != body {
		t.Fatalf("unexpected result: %q", res.Content)
	}
}

// TestGlobTruncatesLargeResult asserts glob stops at budget.GlobEntries.
func TestGlobTruncatesLargeResult(t *testing.T) {
	dir := t.TempDir()
	n := budget.GlobEntries + 50
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)),
			[]byte(""), 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	h := &nullWorkdirHost{wd: dir}
	raw, _ := json.Marshal(map[string]any{"pattern": "*.txt"})
	res, _ := GlobTool{}.Run(context.Background(), raw, h)

	if !strings.Contains(res.Content, "[truncated:") {
		t.Errorf("glob truncation marker missing: %q", res.Content[max0(len(res.Content)-200):])
	}
	if !strings.Contains(res.Content, fmt.Sprintf("of %d matches", n)) {
		t.Errorf("total-count not surfaced: %q", res.Content[max0(len(res.Content)-200):])
	}
}

// TestGrepTruncatesMatchList asserts the in-process grep caps at
// budget.GrepMatches.
func TestGrepTruncatesMatchList(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < budget.GrepMatches+20; i++ {
		lines = append(lines, fmt.Sprintf("needle line %d", i))
	}
	writeTempFile(t, dir, "a.txt", strings.Join(lines, "\n")+"\n")
	h := &nullWorkdirHost{wd: dir}

	raw, _ := json.Marshal(map[string]any{"pattern": "needle"})
	res, _ := GrepTool{}.Run(context.Background(), raw, h)

	if !strings.Contains(res.Content, "[truncated:") {
		t.Errorf("grep truncation marker missing: tail=%q", res.Content[max0(len(res.Content)-200):])
	}
}

func TestGrepRejectsTooManyWalkEntries(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		writeTempFile(t, dir, fmt.Sprintf("f%d.txt", i), "needle\n")
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	_, err = grepRoot(root, ".", "needle", grepWalkLimits{
		maxEntries:  2,
		maxDepth:    maxGrepWalkDepth,
		maxMatches:  budget.GrepMatches,
		maxFileSize: maxGrepFileBytes,
	})
	if err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Fatalf("grepRoot error = %v, want entry cap rejection", err)
	}
}

func TestGrepRejectsTooDeepWalk(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, deep, "target.txt", "needle\n")
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	_, err = grepRoot(root, ".", "needle", grepWalkLimits{
		maxEntries:  10,
		maxDepth:    1,
		maxMatches:  budget.GrepMatches,
		maxFileSize: maxGrepFileBytes,
	})
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds 1") {
		t.Fatalf("grepRoot error = %v, want depth cap rejection", err)
	}
}

func TestGrepStoresOnlyBudgetedMatches(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", strings.Repeat("needle\n", 5))
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	results, err := grepRoot(root, ".", "needle", grepWalkLimits{
		maxEntries:  10,
		maxDepth:    maxGrepWalkDepth,
		maxMatches:  3,
		maxFileSize: maxGrepFileBytes,
	})
	if err != nil {
		t.Fatalf("grepRoot error = %v", err)
	}
	if results.matches != 5 || len(results.lines) != 3 {
		t.Fatalf("matches=%d stored=%d, want matches=5 stored=3", results.matches, len(results.lines))
	}
	if got := formatGrepResults(results); !strings.Contains(got, "[truncated: 3 of 5 matches shown") {
		t.Fatalf("truncation marker missing from %q", got)
	}
}

func TestGlobRejectsEscapingPattern(t *testing.T) {
	dir := t.TempDir()
	h := &nullWorkdirHost{wd: dir}
	raw, _ := json.Marshal(map[string]any{"pattern": "../*"})
	_, err := GlobTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("GlobTool.Run error = %v, want workdir escape rejection", err)
	}
}

func TestGrepRejectsEscapingPath(t *testing.T) {
	dir := t.TempDir()
	h := &nullWorkdirHost{wd: dir}
	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "path": "../"})
	_, err := GrepTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("GrepTool.Run error = %v, want workdir escape rejection", err)
	}
}

// nullWorkdirHost — tests that don't exercise dedup.
type nullWorkdirHost struct{ wd string }

func (h *nullWorkdirHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h *nullWorkdirHost) Workdir() string { return h.wd }
func (h *nullWorkdirHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (h *nullWorkdirHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
