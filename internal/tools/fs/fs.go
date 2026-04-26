package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

type ReadTool struct{}

func (ReadTool) Name() string { return "read" }
func (ReadTool) Description() string {
	return "Read the contents of a file. Optional start/end line numbers (1-indexed, inclusive; end=-1 means EOF)."
}
func (ReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "Path to the file"},
			"start": map[string]any{"type": "integer", "description": "First line to read (1-indexed, inclusive). Omit for full file."},
			"end":   map[string]any{"type": "integer", "description": "Last line to read (1-indexed, inclusive). Omit or set to -1 for EOF."},
		},
		"required": []string{"path"},
	}
}

// Run handles both full-file and ranged reads. For repeated calls against
// the same path+range in a single stado process, returns a terse reference
// response in place of the file bytes when the content hash matches a prior
// read — saves tokens without rewriting prior turns.
// See DESIGN §"Context management" → "In-turn deduplication".
func (ReadTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p ReadArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	// Resolve ranged-read slice. Canonical form for the ReadKey.Range:
	// "" when no start/end were passed, "<start>:<end>" otherwise (EOF
	// preserved as -1 so the key survives file growth).
	raw, err := readToolContent(h.Workdir(), p.Path, p.Start, p.End)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	rangeKey := canonicalRange(p.Start, p.End)

	// Apply the per-tool output budget BEFORE hashing. Hash scope is
	// "the bytes returned to the model" (DESIGN §"Tool-output curation"),
	// so an identical re-truncation hashes identically → dedup still
	// works for large-file re-reads.
	rendered := budget.TruncateBytes(string(raw), budget.ReadBytes,
		fmt.Sprintf("call %s with start=<N> end=<M> to request a specific range", p.Path))

	// Hash the bytes we'd surface to the model. sha256 is pinned for
	// alignment with the audit layer (DESIGN §"Audit") — one hash
	// vocabulary per session.
	hsum := sha256.New()
	_, _ = io.Copy(hsum, strings.NewReader(rendered))
	contentHash := hex.EncodeToString(hsum.Sum(nil))

	key := tool.ReadKey{Path: p.Path, Range: rangeKey}

	// Dedup: if the prior hash for this exact key matches the current
	// hash, return a citation in place of the bytes. The prior turn
	// stays untouched — append-only invariant upheld.
	if prior, ok := h.PriorRead(key); ok && prior.ContentHash == contentHash {
		return tool.Result{Content: referenceResponse(prior, rangeKey)}, nil
	}

	// Fresh read: record + return the bytes.
	h.RecordRead(key, tool.PriorReadInfo{ContentHash: contentHash})
	return tool.Result{Content: rendered}, nil
}

func readToolContent(workdir, path string, start, end *int) ([]byte, error) {
	f, err := workdirpath.OpenReadFile(workdir, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	const maxReadBytes = budget.ReadBytes + 1
	if start == nil && end == nil {
		return io.ReadAll(io.LimitReader(f, int64(maxReadBytes)))
	}
	s, e := resolveBounds(start, end)
	return readLineRangeLimited(f, s, e, maxReadBytes)
}

func readLineRangeLimited(r io.Reader, start, end, maxBytes int) ([]byte, error) {
	buf := make([]byte, 32*1024)
	out := make([]byte, 0, min(maxBytes, budget.ReadBytes))
	line := 1
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			selected := line >= start && (end == -1 || line <= end)
			if b == '\n' {
				if selected && !(end != -1 && line == end) {
					out = append(out, b)
				}
				if end != -1 && line >= end {
					return out, nil
				}
				line++
			} else if selected {
				out = append(out, b)
			}
			if len(out) >= maxBytes {
				return out, nil
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// canonicalRange returns "" for full-file, "<start>:<end>" for ranged.
// A caller that passes neither start nor end gets full-file; passing
// either (even as -1/EOF) produces a ranged key.
func canonicalRange(start, end *int) string {
	if start == nil && end == nil {
		return ""
	}
	s := 1
	if start != nil {
		s = *start
	}
	e := -1
	if end != nil {
		e = *end
	}
	return fmt.Sprintf("%d:%d", s, e)
}

// resolveBounds hydrates 1-indexed inclusive bounds. start defaults to 1;
// end defaults to -1 (EOF). Upstream of sliceLines which resolves -1.
func resolveBounds(start, end *int) (int, int) {
	s := 1
	e := -1
	if start != nil {
		s = *start
	}
	if end != nil {
		e = *end
	}
	if s < 1 {
		s = 1
	}
	return s, e
}

// referenceResponse is the terse citation returned on a dedup hit. Matches
// DESIGN §"Context management": "already read at turn 5" or "already read
// lines 10:20 at turn 5". Lets the model disambiguate full-file from
// ranged hits without inspecting the prior turn.
func referenceResponse(prior tool.PriorReadInfo, rangeKey string) string {
	if rangeKey == "" {
		return fmt.Sprintf("[dedup] already read at turn %d (content unchanged)", prior.Turn)
	}
	return fmt.Sprintf("[dedup] already read lines %s at turn %d (content unchanged)", rangeKey, prior.Turn)
}

type WriteTool struct{}

func (WriteTool) Name() string        { return "write" }
func (WriteTool) Description() string { return "Write content to a file (creates or overwrites)" }
func (WriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "Path to the file"},
			"content": map[string]any{"type": "string", "description": "Content to write"},
		},
		"required": []string{"path", "content"},
	}
}

func (WriteTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p WriteArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if guard, ok := h.(tool.WritePathGuard); ok {
		if err := guard.CheckWritePath(p.Path); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
	}
	if err := workdirpath.WriteFile(h.Workdir(), p.Path, []byte(p.Content), 0o644); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: fmt.Sprintf("Wrote %d bytes to %s", len(p.Content), p.Path)}, nil
}

type EditTool struct{}

const maxEditFileBytes int64 = 4 << 20

func (EditTool) Name() string        { return "edit" }
func (EditTool) Description() string { return "Apply a search/replace edit to a file" }
func (EditTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path to the file"},
			"old":  map[string]any{"type": "string", "description": "Text to find (exact match)"},
			"new":  map[string]any{"type": "string", "description": "Replacement text"},
		},
		"required": []string{"path", "old", "new"},
	}
}

func (EditTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p EditArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if guard, ok := h.(tool.WritePathGuard); ok {
		if err := guard.CheckWritePath(p.Path); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
	}
	content, err := readEditContent(h.Workdir(), p.Path)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	idx := strings.Index(content, p.Old)
	if idx < 0 {
		return tool.Result{Error: fmt.Sprintf("text not found in %s", p.Path)}, nil
	}
	editedLen := int64(len(content)-len(p.Old)) + int64(len(p.New))
	if editedLen > maxEditFileBytes {
		err := fmt.Errorf("edited content exceeds %d bytes: %s", maxEditFileBytes, p.Path)
		return tool.Result{Error: err.Error()}, err
	}
	newContent := content[:idx] + p.New + content[idx+len(p.Old):]
	if err := workdirpath.WriteFile(h.Workdir(), p.Path, []byte(newContent), 0o644); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: fmt.Sprintf("Applied edit to %s", p.Path)}, nil
}

func readEditContent(workdir, path string) (string, error) {
	f, err := workdirpath.OpenReadFile(workdir, path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxEditFileBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxEditFileBytes {
		return "", fmt.Errorf("edit file exceeds %d bytes: %s", maxEditFileBytes, path)
	}
	return string(data), nil
}

type GlobTool struct{}

func (GlobTool) Name() string        { return "glob" }
func (GlobTool) Description() string { return "Find files matching a glob pattern" }
func (GlobTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob pattern"},
		},
		"required": []string{"pattern"},
	}
}

func (GlobTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p GlobArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	matches, err := workdirpath.Glob(h.Workdir(), p.Pattern)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	rel := make([]string, len(matches))
	for i, match := range matches {
		r, _ := filepath.Rel(h.Workdir(), match)
		rel[i] = r
	}
	joined := strings.Join(rel, "\n")
	return tool.Result{Content: budget.TruncateLines(joined, budget.GlobEntries,
		"narrow the pattern to reduce matches")}, nil
}

type GrepTool struct{}

const maxGrepFileBytes int64 = 1 << 20

func (GrepTool) Name() string        { return "grep" }
func (GrepTool) Description() string { return "Search file contents with regex" }
func (GrepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Regex pattern"},
			"path":    map[string]any{"type": "string", "description": "File or directory to search in (default: current dir)"},
		},
		"required": []string{"pattern"},
	}
}

func (GrepTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p GrepArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	searchArg := p.Path
	if p.Path == "" {
		searchArg = "."
	}
	rootPath, searchRel, err := workdirpath.RootRel(h.Workdir(), searchArg, false)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	root, err := workdirpath.OpenRootNoSymlink(rootPath)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer func() { _ = root.Close() }()

	var results []string
	err = iofs.WalkDir(root.FS(), filepath.ToSlash(searchRel), func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxGrepFileBytes {
			return nil
		}
		data, err := workdirpath.ReadRootRegularFileLimited(root, filepath.FromSlash(path), maxGrepFileBytes)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, p.Pattern) {
				results = append(results, fmt.Sprintf("%s:%d:%s", path, i+1, line))
			}
		}
		return nil
	})
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(results) == 0 {
		return tool.Result{Content: "No matches found"}, nil
	}
	joined := strings.Join(results, "\n")
	return tool.Result{Content: budget.TruncateLines(joined, budget.GrepMatches,
		"narrow the pattern or path to reduce matches")}, nil
}

// ReadArgs is the input to ReadTool. Start/End are 1-indexed, inclusive.
// Omit both for a full-file read; pass end=-1 to mean EOF.
type ReadArgs struct {
	Path  string `json:"path"`
	Start *int   `json:"start,omitempty"`
	End   *int   `json:"end,omitempty"`
}

// PathArgs is the legacy alias kept for any external callers. Prefer
// ReadArgs. Deprecated: use ReadArgs.
type PathArgs = ReadArgs

type WriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type EditArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type GlobArgs struct {
	Pattern string `json:"pattern"`
}

type GrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}
