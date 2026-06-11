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

	"github.com/foobarto/stado/internal/fs/hashline"
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
	want := hashline.Render("hello\nworld\n", 1)
	if got != want {
		t.Fatalf("full read = %q, want %q", got, want)
	}
	// Output carries LINE#HASH: prefixes (byte-identical to the engine).
	if !strings.HasPrefix(got, "1#") || !strings.Contains(got, ":hello") {
		t.Fatalf("read output missing hashline prefix: %q", got)
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
	// Ranged read keeps ABSOLUTE line numbers: first rendered line is line 2.
	want := hashline.Render("l2\nl3\nl4", 2)
	if got != want {
		t.Fatalf("range 2:4 = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "2#") {
		t.Fatalf("ranged read should anchor at absolute line 2: %q", got)
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
	if got1 != hashline.Render("body\n", 1) {
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
	if got != hashline.Render("v2\n", 1) {
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

func TestReadRejectsEscapingWorkdir(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "wd")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, root, "secret.txt", "secret")

	h := newRecordingHost(workdir)
	raw, _ := json.Marshal(map[string]any{"path": "../secret.txt"})
	_, err := ReadTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("ReadTool.Run error = %v, want workdir escape rejection", err)
	}
}

func TestWriteRejectsEscapingWorkdir(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "wd")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newRecordingHost(workdir)

	raw, _ := json.Marshal(map[string]any{"path": "../config.toml", "content": "pwned"})
	_, err := WriteTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("WriteTool.Run error = %v, want workdir escape rejection", err)
	}
}

// anchorFor builds a "LINE#HASH" reference for the given 1-indexed line of
// content — the same way the model would copy it out of read output.
func anchorFor(t *testing.T, content string, line int) string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if line < 1 || line > len(lines) {
		t.Fatalf("anchorFor: line %d out of range (file has %d lines)", line, len(lines))
	}
	return fmt.Sprintf("%d#%s", line, hashline.LineHash(line, lines[line-1]))
}

// editArgs marshals a hashline edit request.
func editArgs(t *testing.T, path string, edits ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"path": path, "edits": edits})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestEditRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "wd")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workdir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	h := newRecordingHost(workdir)
	raw := editArgs(t, "link.txt", map[string]any{
		"op":    "replace",
		"pos":   anchorFor(t, "secret\n", 1),
		"lines": []string{"edited"},
	})
	_, err := EditTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("EditTool.Run error = %v, want symlink escape rejection", err)
	}
}

func TestEditAppliesSmallFile(t *testing.T) {
	dir := t.TempDir()
	content := "hello\nworld\n"
	writeTempFile(t, dir, "a.txt", content)

	h := newRecordingHost(dir)
	raw := editArgs(t, "a.txt", map[string]any{
		"op":    "replace",
		"pos":   anchorFor(t, content, 2),
		"lines": []string{"stado"},
	})
	res, err := EditTool{}.Run(context.Background(), raw, h)
	if err != nil {
		t.Fatalf("EditTool.Run error = %v", err)
	}
	if res.Error != "" {
		t.Fatalf("EditTool.Run result error = %q", res.Error)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\nstado\n" {
		t.Fatalf("edited file = %q", got)
	}
}

// TestEditRangeReplace exercises a two-anchor range replace.
func TestEditRangeReplace(t *testing.T) {
	dir := t.TempDir()
	content := "a\nb\nc\nd\n"
	writeTempFile(t, dir, "a.txt", content)

	h := newRecordingHost(dir)
	raw := editArgs(t, "a.txt", map[string]any{
		"op":    "replace",
		"pos":   anchorFor(t, content, 2),
		"end":   anchorFor(t, content, 3),
		"lines": []string{"X", "Y", "Z"},
	})
	res, err := EditTool{}.Run(context.Background(), raw, h)
	if err != nil || res.Error != "" {
		t.Fatalf("EditTool.Run err=%v result=%q", err, res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "a\nX\nY\nZ\nd\n" {
		t.Fatalf("range replace = %q", got)
	}
}

// TestEditStaleAnchorRejected: a wrong hash is rejected with fresh anchors
// and the file is left unchanged.
func TestEditStaleAnchorRejected(t *testing.T) {
	dir := t.TempDir()
	content := "hello\nworld\n"
	writeTempFile(t, dir, "a.txt", content)

	wrong := "KT"
	if wrong == hashline.LineHash(2, "world") {
		wrong = "PZ"
	}
	h := newRecordingHost(dir)
	raw := editArgs(t, "a.txt", map[string]any{
		"op":    "replace",
		"pos":   fmt.Sprintf("2#%s", wrong),
		"lines": []string{"x"},
	})
	res, err := EditTool{}.Run(context.Background(), raw, h)
	if err != nil {
		t.Fatalf("stale anchor should be a tool-surface error, not a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "E_STALE_ANCHOR") {
		t.Fatalf("expected stale-anchor error, got %q", res.Error)
	}
	// Fresh anchors carry the correct current hash for line 2.
	if !strings.Contains(res.Error, fmt.Sprintf("2#%s", hashline.LineHash(2, "world"))) {
		t.Fatalf("fresh anchors missing correct ref: %q", res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != content {
		t.Fatalf("file must be unchanged on stale anchor, got %q", got)
	}
}

// TestEditRejectsDisplayPrefixedLines: the literal-content guard.
func TestEditRejectsDisplayPrefixedLines(t *testing.T) {
	dir := t.TempDir()
	content := "hello\nworld\n"
	writeTempFile(t, dir, "a.txt", content)

	h := newRecordingHost(dir)
	raw := editArgs(t, "a.txt", map[string]any{
		"op":    "replace",
		"pos":   anchorFor(t, content, 1),
		"lines": []string{"2#KT:world"}, // copied display prefix, not literal content
	})
	res, err := EditTool{}.Run(context.Background(), raw, h)
	if err != nil {
		t.Fatalf("display-prefix should be a tool-surface error: %v", err)
	}
	if !strings.Contains(res.Error, "E_INVALID_PATCH") {
		t.Fatalf("expected invalid-patch rejection, got %q", res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != content {
		t.Fatalf("file must be unchanged when patch rejected, got %q", got)
	}
}

// TestEditMultiEditBottomUp: multiple replaces in one call apply against the
// original line numbers (bottom-up), even when an earlier edit grows the file.
func TestEditMultiEditBottomUp(t *testing.T) {
	dir := t.TempDir()
	content := "l1\nl2\nl3\nl4\nl5\n"
	writeTempFile(t, dir, "a.txt", content)

	h := newRecordingHost(dir)
	raw := editArgs(t, "a.txt",
		map[string]any{"op": "replace", "pos": anchorFor(t, content, 2), "lines": []string{"a", "b", "c"}},
		map[string]any{"op": "replace", "pos": anchorFor(t, content, 4), "lines": []string{"D"}},
	)
	res, err := EditTool{}.Run(context.Background(), raw, h)
	if err != nil || res.Error != "" {
		t.Fatalf("EditTool.Run err=%v result=%q", err, res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "l1\na\nb\nc\nl3\nD\nl5\n" {
		t.Fatalf("multi-edit bottom-up = %q", got)
	}
}

func TestEditRejectsOversizedSourceFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxEditFileBytes+1); err != nil {
		t.Fatal(err)
	}

	h := newRecordingHost(dir)
	raw := editArgs(t, "huge.txt", map[string]any{"op": "replace", "pos": "1#KT", "lines": []string{"y"}})
	_, err := EditTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("EditTool.Run error = %v, want size rejection", err)
	}
}

func TestEditRejectsOversizedResult(t *testing.T) {
	dir := t.TempDir()
	content := "x\n"
	writeTempFile(t, dir, "a.txt", content)

	h := newRecordingHost(dir)
	bigLine := strings.Repeat("y", int(maxEditFileBytes)+1)
	raw := editArgs(t, "a.txt", map[string]any{
		"op":    "replace",
		"pos":   anchorFor(t, content, 1),
		"lines": []string{bigLine},
	})
	_, err := EditTool{}.Run(context.Background(), raw, h)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("EditTool.Run error = %v, want result size rejection", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("file should be unchanged, got %q", got)
	}
}
