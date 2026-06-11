package lspfind

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/lsp"
	"github.com/foobarto/stado/internal/workdirpath"
)

// diagnosticsWaitDefault bounds how long DiagnosticsViaManager blocks for
// the server's first publish after re-opening an edited file. A few
// seconds covers a warm gopls / pyright republishing a single touched
// file; a cold index that takes longer simply yields whatever's cached
// (often empty) and the next edit picks up the freshly-indexed set. The
// post-edit hook must not stall the agent loop, so this is intentionally
// short.
const diagnosticsWaitDefault = 3 * time.Second

// DiagnosticsStore holds the latest LSP diagnostics per file for one
// session. The post-edit hook writes the set for each touched file after a
// Mutating edit/write; the TUI render goroutine reads the summary to draw
// the diagnostics sidebar panel. Thread-safe: a sync.RWMutex guards the
// map so the render path (read) doesn't block the hook (write) beyond the
// brief map swap, mirroring LSPClientManager's concurrency posture.
//
// Keys are workdir-relative slash paths (the same form the TUI shows), so
// the same file edited via different absolute spellings collapses to one
// entry. The zero value is NOT usable — construct with NewDiagnosticsStore.
type DiagnosticsStore struct {
	mu    sync.RWMutex
	files map[string]FileDiagnostics // rel path → latest set
}

// FileDiagnostics is one file's current diagnostics plus pre-computed
// severity counts so the render path needn't re-walk the slice every frame.
type FileDiagnostics struct {
	// RelPath is the workdir-relative slash path used as the store key and
	// shown in the surface.
	RelPath     string
	Diagnostics []lsp.Diagnostic
	Errors      int
	Warnings    int
}

// NewDiagnosticsStore returns an empty session-scoped store.
func NewDiagnosticsStore() *DiagnosticsStore {
	return &DiagnosticsStore{files: map[string]FileDiagnostics{}}
}

// Set records the latest diagnostics for relPath. An empty slice clears
// the file's problems (the file is dropped from the store so a clean file
// doesn't linger as a zero-count entry in the surface). Severity counts
// are computed once here, off the render path.
func (s *DiagnosticsStore) Set(relPath string, diags []lsp.Diagnostic) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(diags) == 0 {
		delete(s.files, relPath)
		return
	}
	fd := FileDiagnostics{RelPath: relPath, Diagnostics: diags}
	for _, d := range diags {
		switch d.Severity {
		case lsp.SeverityError:
			fd.Errors++
		case lsp.SeverityWarning:
			fd.Warnings++
		}
	}
	s.files[relPath] = fd
}

// Reset drops every file's diagnostics. Called on a session switch so the
// incoming session's surface doesn't show the previous session's problems.
func (s *DiagnosticsStore) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.files = map[string]FileDiagnostics{}
	s.mu.Unlock()
}

// Summary is the render-ready snapshot the TUI draws: total error/warning
// counts across all files and a severity-ranked, capped list of individual
// diagnostics. Truncated is true when the list was capped (the caller logs
// it); Total is the pre-cap count.
type Summary struct {
	Errors    int
	Warnings  int
	Entries   []DiagnosticEntry
	Total     int
	Truncated bool
}

// DiagnosticEntry is one rendered diagnostic line for the surface:
// `rel/path.go:LINE` plus the severity and message, ready to colour by tone.
type DiagnosticEntry struct {
	RelPath  string
	Line     int // 1-indexed for display
	Severity lsp.Severity
	Message  string
}

// Summarize returns the current store snapshot capped at maxEntries,
// ranked errors-before-warnings-before-the-rest and then by file:line so
// the surface is stable across frames. maxEntries <= 0 means "no cap".
func (s *DiagnosticsStore) Summarize(maxEntries int) Summary {
	if s == nil {
		return Summary{}
	}
	s.mu.RLock()
	files := make([]FileDiagnostics, 0, len(s.files))
	for _, fd := range s.files {
		files = append(files, fd)
	}
	s.mu.RUnlock()

	var sum Summary
	all := make([]DiagnosticEntry, 0)
	for _, fd := range files {
		sum.Errors += fd.Errors
		sum.Warnings += fd.Warnings
		for _, d := range fd.Diagnostics {
			all = append(all, DiagnosticEntry{
				RelPath:  fd.RelPath,
				Line:     d.Range.Start.Line + 1,
				Severity: d.Severity,
				Message:  d.Message,
			})
		}
	}
	sum.Total = len(all)

	// Rank by severity (error < warning < info < hint < unknown — i.e. most
	// severe first), then file, then line, for a deterministic top-N.
	sort.SliceStable(all, func(i, j int) bool {
		si, sj := severityRank(all[i].Severity), severityRank(all[j].Severity)
		if si != sj {
			return si < sj
		}
		if all[i].RelPath != all[j].RelPath {
			return all[i].RelPath < all[j].RelPath
		}
		return all[i].Line < all[j].Line
	})

	if maxEntries > 0 && len(all) > maxEntries {
		sum.Truncated = true
		all = all[:maxEntries]
	}
	sum.Entries = all
	return sum
}

// severityRank orders severities most-severe-first for the top-N cut.
// SeverityUnknown (0) sorts last so a server that omits severity doesn't
// crowd out real errors.
func severityRank(s lsp.Severity) int {
	switch s {
	case lsp.SeverityError:
		return 0
	case lsp.SeverityWarning:
		return 1
	case lsp.SeverityInfo:
		return 2
	case lsp.SeverityHint:
		return 3
	default:
		return 4
	}
}

// DiagnosticsViaManager re-opens path against the session-scoped manager's
// language server and returns the server's published diagnostics for it.
// Returns nil (no error) when the file's extension has no configured
// server — a non-LSP file edit is simply not diagnosed, not an error.
//
// It mirrors the other *ViaManager helpers: resolve the path under the
// workdir, map the extension to a server, get/launch the client, didOpen +
// wait for the push. The wait is bounded by ctx (or diagnosticsWaitDefault
// when ctx has no sooner deadline) so a slow/cold server can't stall the
// caller.
func DiagnosticsViaManager(ctx context.Context, m *LSPClientManager, path, workdir string) ([]lsp.Diagnostic, error) {
	ext := filepath.Ext(path)
	server := serverFor(ext)
	if server == "" {
		return nil, nil
	}
	r, err := workdirpath.New(workdir)
	if err != nil {
		return nil, err
	}
	full, err := r.Resolve(path)
	if err != nil {
		return nil, err
	}
	cli, err := m.ClientFor(ctx, workdir, server)
	if err != nil {
		return nil, err
	}
	text, err := readLSPDocumentText(workdir, path)
	if err != nil {
		return nil, err
	}

	// Bound the publish wait. If the caller's ctx already has a sooner
	// deadline, respect it; otherwise impose our own short cap so the hook
	// returns promptly.
	waitCtx := ctx
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > diagnosticsWaitDefault {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, diagnosticsWaitDefault)
		defer cancel()
	}

	return cli.Diagnostics(waitCtx, full, languageIDFor(ext), text)
}

// relPathFor renders the workdir-relative slash path for a file, used as
// the diagnostics-store key + surface label. Falls back to the input on
// any resolution failure so a diagnostic is never dropped for a pathing
// quirk.
func relPathFor(workdir, path string) string {
	r, err := workdirpath.New(workdir)
	if err != nil {
		return path
	}
	full, err := r.Resolve(path)
	if err != nil {
		return path
	}
	_, rel, err := r.RootRel(full)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// String renders a one-line summary header for logging / debug surfaces:
// `N errors · M warnings` (omitting a zero half). Empty when both are zero.
func (sum Summary) String() string {
	switch {
	case sum.Errors > 0 && sum.Warnings > 0:
		return fmt.Sprintf("%d %s · %d %s", sum.Errors, plural(sum.Errors, "error"), sum.Warnings, plural(sum.Warnings, "warning"))
	case sum.Errors > 0:
		return fmt.Sprintf("%d %s", sum.Errors, plural(sum.Errors, "error"))
	case sum.Warnings > 0:
		return fmt.Sprintf("%d %s", sum.Warnings, plural(sum.Warnings, "warning"))
	default:
		return ""
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
