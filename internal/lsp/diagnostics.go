package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
)

// Severity is the LSP DiagnosticSeverity enum (1=Error … 4=Hint). The
// wire values are fixed by the spec; we keep them as named constants so
// callers (the diagnostics store, the TUI surface) don't sprinkle magic
// numbers. Anything outside 1–4 is treated as SeverityUnknown.
type Severity int

const (
	SeverityUnknown Severity = 0
	SeverityError   Severity = 1
	SeverityWarning Severity = 2
	SeverityInfo    Severity = 3
	SeverityHint    Severity = 4
)

// String renders a short, stable label used in the TUI surface.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warn"
	case SeverityInfo:
		return "info"
	case SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// Diagnostic is one published problem on a file: a 0-indexed range plus a
// severity and message. Line/Character mirror the LSP Position (0-indexed,
// UTF-16); callers add 1 for human-facing file:line display.
type Diagnostic struct {
	Range    Range    `json:"range"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Source   string   `json:"source,omitempty"`
}

// publishDiagnosticsParams is the wire shape of a
// textDocument/publishDiagnostics notification.
type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// handlePublishDiagnostics records the latest published set for a file and
// wakes any Diagnostics() waiter. Called from readLoop on the server's
// unsolicited push. A publish with an empty list (the server cleared the
// file's problems) is recorded as an empty slice, not dropped, so a reader
// learns "no problems now" rather than seeing a stale set.
func (c *Client) handlePublishDiagnostics(raw json.RawMessage) {
	var p publishDiagnosticsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	path := URIToPath(p.URI)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	c.diagMu.Lock()
	if c.diags == nil {
		c.diags = map[string][]Diagnostic{}
	}
	// Never store nil — an explicit empty slice means "published, none".
	if p.Diagnostics == nil {
		p.Diagnostics = []Diagnostic{}
	}
	c.diags[path] = p.Diagnostics
	waiters := c.diagWaiters
	c.diagWaiters = nil
	c.diagMu.Unlock()

	for _, w := range waiters {
		// Buffered (cap 1) channel: never blocks the read loop.
		select {
		case w <- path:
		default:
		}
	}
}

// closeDiagWaiters wakes every pending Diagnostics() waiter on server
// death so the caller stops blocking. The empty path signals "no fresh
// publish, server gone — return the cache".
func (c *Client) closeDiagWaiters() {
	c.diagMu.Lock()
	waiters := c.diagWaiters
	c.diagWaiters = nil
	c.diagMu.Unlock()
	for _, w := range waiters {
		select {
		case w <- "":
		default:
		}
	}
}

// CachedDiagnostics returns the last-published diagnostics for path
// without contacting the server. Returns nil if the server never
// published for that file. The returned slice is a copy — safe to retain
// past a later publish.
func (c *Client) CachedDiagnostics(path string) []Diagnostic {
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	ds, ok := c.diags[abs]
	if !ok {
		return nil
	}
	out := make([]Diagnostic, len(ds))
	copy(out, ds)
	return out
}

// Diagnostics opens path against the server (didOpen), then waits up to
// ctx's deadline for the server's first textDocument/publishDiagnostics
// push for that file, returning the published set. A server that publishes
// nothing (no problems, or doesn't push within the deadline) yields the
// already-cached set (possibly empty/nil). This is the push model: we
// don't issue a textDocument/diagnostic pull request.
//
// languageID + text are passed straight to DidOpen; callers that already
// opened the document still benefit (most servers re-publish on a repeat
// didOpen). ctx scopes only the wait — the document is opened regardless.
func (c *Client) Diagnostics(ctx context.Context, path, languageID, text string) ([]Diagnostic, error) {
	abs, _ := filepath.Abs(path)

	// Register a waiter BEFORE the didOpen so a fast publish (server
	// already had the file indexed) can't slip in between open and wait.
	w := make(chan string, 1)
	c.diagMu.Lock()
	c.diagWaiters = append(c.diagWaiters, w)
	c.diagMu.Unlock()

	if err := c.DidOpen(abs, languageID, text); err != nil {
		// Drop our waiter — DidOpen failed, no publish is coming for it.
		c.dropDiagWaiter(w)
		return nil, err
	}

	for {
		select {
		case got := <-w:
			// Any publish (for our file or another in the same server)
			// or a server-death signal ("") lands here. If it's ours,
			// return it; otherwise re-arm and keep waiting until the
			// deadline. The cache read covers the "another file
			// published, ours was indexed too" case.
			if got == abs || got == "" {
				return c.CachedDiagnostics(abs), nil
			}
			// Re-arm: a different file published; our file might have too
			// (gopls publishes per-file). Check the cache, else wait on.
			if cached := c.CachedDiagnostics(abs); cached != nil {
				return cached, nil
			}
			w = make(chan string, 1)
			c.diagMu.Lock()
			c.diagWaiters = append(c.diagWaiters, w)
			c.diagMu.Unlock()
		case <-ctx.Done():
			c.dropDiagWaiter(w)
			// Deadline hit: return whatever the server already pushed.
			return c.CachedDiagnostics(abs), nil
		}
	}
}

// dropDiagWaiter removes w from the pending-waiter set (best effort): used
// when a Diagnostics call abandons its wait (DidOpen error / ctx done) so
// the slice doesn't grow unbounded across many calls.
func (c *Client) dropDiagWaiter(w chan string) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	for i, x := range c.diagWaiters {
		if x == w {
			c.diagWaiters = append(c.diagWaiters[:i], c.diagWaiters[i+1:]...)
			return
		}
	}
}
