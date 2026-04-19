package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// recordingHost captures PriorRead/RecordRead traffic for dedup assertions.
// It's intentionally standalone — we don't pull in internal/tools.ReadLog
// because that'd introduce a test import cycle.
type recordingHost struct {
	mu      sync.Mutex
	wd      string
	entries map[tool.ReadKey]tool.PriorReadInfo
	turn    int
}

func newRecordingHost(wd string) *recordingHost {
	return &recordingHost{wd: wd, entries: make(map[tool.ReadKey]tool.PriorReadInfo), turn: 1}
}

func (h *recordingHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h *recordingHost) Workdir() string { return h.wd }

func (h *recordingHost) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	info, ok := h.entries[key]
	return info, ok
}

func (h *recordingHost) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if info.Turn == 0 {
		info.Turn = h.turn
	}
	h.entries[key] = info
}

func writeTempFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func invokeRead(t *testing.T, h tool.Host, args map[string]any) (string, string) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := ReadTool{}.Run(context.Background(), raw, h)
	if err != nil {
		t.Fatalf("ReadTool.Run: %v", err)
	}
	return res.Content, res.Error
}

// TestReadFullFile is the baseline full-file happy path.
func TestReadFullFile(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "hello\nworld\n")

	h := newRecordingHost(dir)
	got, _ := invokeRead(t, h, map[string]any{"path": "a.txt"})
	if got != "hello\nworld\n" {
		t.Fatalf("full read: %q", got)
	}
	// First read records the key with canonical Range "" (full-file).
	if _, ok := h.entries[tool.ReadKey{Path: "a.txt", Range: ""}]; !ok {
		t.Fatalf("expected recording under Range=\"\", got: %v", h.entries)
	}
}

// TestReadRangedLines verifies 1-indexed inclusive slicing.
func TestReadRangedLines(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "l1\nl2\nl3\nl4\nl5\n")

	h := newRecordingHost(dir)
	got, _ := invokeRead(t, h, map[string]any{"path": "a.txt", "start": 2, "end": 4})
	want := "l2\nl3\nl4"
	if got != want {
		t.Fatalf("range 2:4 = %q, want %q", got, want)
	}
	if _, ok := h.entries[tool.ReadKey{Path: "a.txt", Range: "2:4"}]; !ok {
		t.Fatalf("expected key 2:4, got: %v", h.entries)
	}
}

// TestReadEOFSentinelPreserved asserts end=-1 stays -1 in the Range key so
// the key doesn't drift as the file grows.
func TestReadEOFSentinelPreserved(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "l1\nl2\nl3\n")

	h := newRecordingHost(dir)
	_, _ = invokeRead(t, h, map[string]any{"path": "a.txt", "start": 2, "end": -1})
	if _, ok := h.entries[tool.ReadKey{Path: "a.txt", Range: "2:-1"}]; !ok {
		t.Fatalf("expected key 2:-1, got: %v", h.entries)
	}
}

// TestReadDedupSameContent is the happy path: second read of the same
// unchanged file returns a reference response.
func TestReadDedupSameContent(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "body\n")

	h := newRecordingHost(dir)
	got1, _ := invokeRead(t, h, map[string]any{"path": "a.txt"})
	if got1 != "body\n" {
		t.Fatalf("first read: %q", got1)
	}

	h.turn = 2 // simulate next turn
	got2, _ := invokeRead(t, h, map[string]any{"path": "a.txt"})
	if !strings.Contains(got2, "[dedup]") {
		t.Fatalf("expected dedup response, got: %q", got2)
	}
	if !strings.Contains(got2, "turn 1") {
		t.Fatalf("dedup response should cite prior turn 1, got: %q", got2)
	}
}

// TestReadDedupStaleOnChange asserts changed content breaks dedup.
func TestReadDedupStaleOnChange(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "v1\n")

	h := newRecordingHost(dir)
	_, _ = invokeRead(t, h, map[string]any{"path": "a.txt"})

	// Modify file — next read should be fresh, not deduped.
	writeTempFile(t, dir, "a.txt", "v2\n")
	h.turn = 2
	got, _ := invokeRead(t, h, map[string]any{"path": "a.txt"})
	if strings.Contains(got, "[dedup]") {
		t.Fatalf("changed file must not dedup, got: %q", got)
	}
	if got != "v2\n" {
		t.Fatalf("fresh read: %q", got)
	}
}

// TestReadDedupDistinctKeys ensures full-file and ranged reads of the same
// path don't dedup against each other (per DESIGN "exact path-and-range
// match only").
func TestReadDedupDistinctKeys(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "l1\nl2\nl3\n")

	h := newRecordingHost(dir)
	_, _ = invokeRead(t, h, map[string]any{"path": "a.txt"})

	h.turn = 2
	got, _ := invokeRead(t, h, map[string]any{"path": "a.txt", "start": 1, "end": 2})
	if strings.Contains(got, "[dedup]") {
		t.Fatalf("ranged read must not dedup against prior full-file read, got: %q", got)
	}
	// Now we have two entries under different canonical keys.
	if len(h.entries) != 2 {
		t.Fatalf("expected 2 entries (full + 1:2), got: %d → %v", len(h.entries), h.entries)
	}
}

// TestReadCanonicalRangeShapes exhaustively checks the canonical-range
// rule for each input shape the tool accepts. PLAN §11.4.10 requires
// this per-shape assertion.
func TestReadCanonicalRangeShapes(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.txt", "l1\nl2\nl3\n")

	cases := []struct {
		name      string
		args      map[string]any
		wantRange string
	}{
		{"no-bounds", map[string]any{"path": "a.txt"}, ""},
		{"start-only", map[string]any{"path": "a.txt", "start": 2}, "2:-1"},
		{"end-only", map[string]any{"path": "a.txt", "end": 2}, "1:2"},
		{"both", map[string]any{"path": "a.txt", "start": 1, "end": 3}, "1:3"},
		{"end-eof", map[string]any{"path": "a.txt", "start": 1, "end": -1}, "1:-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRecordingHost(dir)
			_, _ = invokeRead(t, h, tc.args)
			if _, ok := h.entries[tool.ReadKey{Path: "a.txt", Range: tc.wantRange}]; !ok {
				t.Fatalf("expected key Range=%q, got: %v", tc.wantRange, h.entries)
			}
		})
	}
}

// TestReadConcurrentDoesNotCorruptLog fires many parallel reads of distinct
// keys and asserts the recorder ends up with every key present, each with
// a non-empty ContentHash.
func TestReadConcurrentDoesNotCorruptLog(t *testing.T) {
	dir := t.TempDir()
	// Create 20 tiny files.
	for i := 0; i < 20; i++ {
		writeTempFile(t, dir, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("body-%d\n", i))
	}
	h := newRecordingHost(dir)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = invokeRead(t, h, map[string]any{"path": fmt.Sprintf("f%d.txt", i)})
		}(i)
	}
	wg.Wait()

	if len(h.entries) != 20 {
		t.Fatalf("expected 20 entries, got %d", len(h.entries))
	}
	for k, v := range h.entries {
		if v.ContentHash == "" {
			t.Fatalf("entry %v missing ContentHash", k)
		}
	}
}
