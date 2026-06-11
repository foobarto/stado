package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/fs/hashline"
	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

type ReadTool struct{}

func (ReadTool) Class() tool.Class { return tool.ClassNonMutating }
func (ReadTool) Name() string      { return "read" }
func (ReadTool) Description() string {
	return "Read the contents of a file. Each line is prefixed \"LINE#HASH:\" (1-indexed line number + a 2-character content hash). Copy those LINE#HASH anchors into the edit tool's pos/end fields to make hash-validated edits. Optional start/end line numbers (1-indexed, inclusive; end=-1 means EOF) — anchors stay absolute across ranged reads."
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

	// Prefix every line with its LINE#HASH anchor before truncation. The
	// first line's absolute number is the resolved range start (1 for a
	// full-file read), so anchors stay absolute across paged reads — the
	// edit tool validates pos/end against these exact hashes. Same engine
	// the wasm fs plugin uses, so anchors are byte-identical across both.
	startLine := 1
	if p.Start != nil && *p.Start > 1 {
		startLine = *p.Start
	}
	prefixed := hashline.Render(string(raw), startLine)

	// Apply the per-tool output budget AFTER prefixing, BEFORE hashing.
	// Hash scope is "the bytes returned to the model" (DESIGN §"Tool-output
	// curation"), so an identical re-truncation hashes identically → dedup
	// still works for large-file re-reads.
	rendered := budget.TruncateBytes(prefixed, budget.ReadBytes,
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
	r, err := workdirpath.New(workdir)
	if err != nil {
		return nil, err
	}
	f, err := r.OpenRegularFile(path)
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

func (WriteTool) Class() tool.Class   { return tool.ClassMutating }
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
	r, err := workdirpath.New(h.Workdir())
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if err := r.WriteFileAtomic(p.Path, []byte(p.Content), 0o644); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: fmt.Sprintf("Wrote %d bytes to %s", len(p.Content), p.Path)}, nil
}

type EditTool struct{}

const maxEditFileBytes int64 = 4 << 20

func (EditTool) Class() tool.Class { return tool.ClassMutating }
func (EditTool) Name() string      { return "edit" }
func (EditTool) Description() string {
	return "Apply content-anchored (hashline) edits to a file. Each edit targets a LINE#HASH anchor copied verbatim from read output — e.g. {\"op\":\"replace\",\"pos\":\"11#KT\",\"lines\":[...]}. The hash is validated against the file's current content; if it has changed since you read it the edit is REJECTED with fresh anchors to retry (never silently relocated). \"lines\" must be literal file content with NO \"LINE#HASH:\" or diff \"+/-\" prefixes. Ops: replace (pos, optional end for a range), append (after pos / at EOF), prepend (before pos / at BOF). Edits in one call apply bottom-up so line numbers stay valid."
}
func (EditTool) Schema() map[string]any {
	anchorDesc := "LINE#HASH anchor copied from read output, e.g. \"11#KT\""
	editItem := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":  map[string]any{"type": "string", "enum": []string{"replace", "append", "prepend"}, "description": "Operation"},
			"pos": map[string]any{"type": "string", "description": anchorDesc},
			"end": map[string]any{"type": "string", "description": "End anchor for a range replace (inclusive); same LINE#HASH form as pos"},
			"lines": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Literal replacement/insertion lines — NO LINE#HASH: or diff +/- prefixes",
			},
		},
		"required": []string{"op", "lines"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "Path to the file"},
			"edits": map[string]any{"type": "array", "items": editItem, "description": "One or more hashline edits, applied bottom-up"},
		},
		"required": []string{"path", "edits"},
	}
}

func (EditTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p EditArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if len(p.Edits) == 0 {
		return tool.Result{Error: "edit requires at least one entry in \"edits\""}, nil
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

	edits := make([]hashline.Edit, len(p.Edits))
	for i, e := range p.Edits {
		edits[i] = hashline.Edit{Op: hashline.Op(e.Op), Pos: e.Pos, End: e.End, Lines: e.Lines}
	}
	newContent, err := hashline.Apply(content, edits)
	if err != nil {
		// Stale-anchor and validation errors are tool-surface errors the
		// model should see and act on (retry with fresh anchors), not Go
		// errors that abort the turn.
		return tool.Result{Error: err.Error()}, nil
	}
	if int64(len(newContent)) > maxEditFileBytes {
		err := fmt.Errorf("edited content exceeds %d bytes: %s", maxEditFileBytes, p.Path)
		return tool.Result{Error: err.Error()}, err
	}
	r, err := workdirpath.New(h.Workdir())
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if err := r.WriteFileAtomic(p.Path, []byte(newContent), 0o644); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: fmt.Sprintf("Applied %d edit(s) to %s", len(p.Edits), p.Path)}, nil
}

func readEditContent(workdir, path string) (string, error) {
	r, err := workdirpath.New(workdir)
	if err != nil {
		return "", err
	}
	f, err := r.OpenRegularFile(path)
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
	r, err := workdirpath.New(h.Workdir())
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	matches, total, err := r.GlobLimited(p.Pattern, budget.GlobEntries)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	rootPath, err := filepath.EvalSymlinks(h.Workdir())
	if err != nil {
		rootPath = h.Workdir()
	}
	rel := make([]string, len(matches))
	for i, match := range matches {
		r, _ := filepath.Rel(rootPath, match)
		rel[i] = r
	}
	joined := strings.Join(rel, "\n")
	if total > len(rel) {
		if joined != "" {
			joined += "\n"
		}
		joined += fmt.Sprintf("[truncated: %d of %d matches shown — narrow the pattern to reduce matches]", len(rel), total)
	}
	return tool.Result{Content: joined}, nil
}

type GrepTool struct{}

const (
	maxGrepFileBytes   int64 = 1 << 20
	maxGrepWalkEntries       = 200000
	maxGrepWalkDepth         = 128
	grepReadDirBatch         = 128
)

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
	r, err := workdirpath.New(h.Workdir())
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	rootPath, searchRel, err := r.RootRel(searchArg)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(rootPath)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer func() { _ = root.Close() }()

	results, err := grepRoot(root, searchRel, p.Pattern, defaultGrepWalkLimits())
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: formatGrepResults(results)}, nil
}

type grepWalkLimits struct {
	maxEntries  int
	maxDepth    int
	maxMatches  int
	maxFileSize int64
}

type grepResults struct {
	lines   []string
	matches int
}

type grepWalkState struct {
	grepWalkLimits
	entries int
	results grepResults
}

func defaultGrepWalkLimits() grepWalkLimits {
	return grepWalkLimits{
		maxEntries:  maxGrepWalkEntries,
		maxDepth:    maxGrepWalkDepth,
		maxMatches:  budget.GrepMatches,
		maxFileSize: maxGrepFileBytes,
	}
}

func grepRoot(root *os.Root, searchRel, pattern string, limits grepWalkLimits) (grepResults, error) {
	if root == nil {
		return grepResults{}, fmt.Errorf("grep root unavailable")
	}
	searchRel = filepath.Clean(searchRel)
	if searchRel == "" {
		searchRel = "."
	}
	state := &grepWalkState{grepWalkLimits: limits}
	if err := grepWalkPath(root, searchRel, pattern, state, 0); err != nil {
		return grepResults{}, err
	}
	return state.results, nil
}

func grepWalkPath(root *os.Root, rel, pattern string, state *grepWalkState, depth int) error {
	if depth > state.maxDepth {
		return fmt.Errorf("grep walk nesting exceeds %d: %s", state.maxDepth, filepath.ToSlash(rel))
	}
	state.entries++
	if state.entries > state.maxEntries {
		return fmt.Errorf("grep walk contains more than %d entries", state.maxEntries)
	}

	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		return grepWalkDir(root, rel, pattern, state, depth)
	}
	if !info.Mode().IsRegular() || info.Size() > state.maxFileSize {
		return nil
	}
	return grepFile(root, rel, pattern, state)
}

func grepWalkDir(root *os.Root, rel, pattern string, state *grepWalkState, depth int) error {
	dir, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	openedInfo, err := dir.Stat()
	if err != nil {
		return err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !openedInfo.IsDir() {
		return nil
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("grep walk directory changed while opening: %s", filepath.ToSlash(rel))
	}

	for {
		entries, readErr := dir.ReadDir(grepReadDirBatch)
		for _, entry := range entries {
			name := entry.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return fmt.Errorf("grep walk invalid entry name %q", name)
			}
			childRel := name
			if rel != "." {
				childRel = filepath.Join(rel, name)
			}
			if err := grepWalkPath(root, childRel, pattern, state, depth+1); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func grepFile(root *os.Root, rel, pattern string, state *grepWalkState) error {
	data, err := workdirpath.NewRootResolver(root).ReadFileLimited(rel, state.maxFileSize)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, pattern) {
			state.results.matches++
			if len(state.results.lines) < state.maxMatches {
				state.results.lines = append(state.results.lines, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), i+1, line))
			}
		}
	}
	return nil
}

func formatGrepResults(results grepResults) string {
	if results.matches == 0 {
		return "No matches found"
	}
	if results.matches <= len(results.lines) {
		return strings.Join(results.lines, "\n")
	}
	lines := append([]string(nil), results.lines...)
	lines = append(lines, fmt.Sprintf("[truncated: %d of %d matches shown — narrow the pattern or path to reduce matches]",
		len(results.lines), results.matches))
	return strings.Join(lines, "\n")
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

// EditArgs is the input to EditTool — the hashline anchor schema. Each
// EditItem targets a LINE#HASH anchor from read output.
type EditArgs struct {
	Path  string     `json:"path"`
	Edits []EditItem `json:"edits"`
}

// EditItem is one hashline operation. Pos/End are LINE#HASH reference
// strings (End optional, range replace). Lines is literal content with no
// display prefixes. See internal/fs/hashline for the contract.
type EditItem struct {
	Op    string   `json:"op"`
	Pos   string   `json:"pos,omitempty"`
	End   string   `json:"end,omitempty"`
	Lines []string `json:"lines"`
}

type GlobArgs struct {
	Pattern string `json:"pattern"`
}

type GrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}
