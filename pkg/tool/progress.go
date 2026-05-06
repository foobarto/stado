// Per-tool-call progress collector. EP-0038i — agent-loop integration.
//
// stado_progress emissions go to two consumers: the operator surface
// (ProgressEmitter, e.g. TUI sidebar) and the model. The operator
// surface gets the LIVE stream; the model gets the trail prepended
// to the tool result envelope so it has full context when reasoning
// about what the tool did.
//
// The collector is threaded via context.Context so the bundled-plugin
// Run path can append without taking a structural dependency on the
// agent loop. Executor.Run installs a fresh collector per dispatch
// and drains it after the tool returns.
package tool

import (
	"context"
	"sync"
)

// progressCtxKey scopes progress-collector lookups in context.Context.
type progressCtxKey struct{}

// ProgressEntry is one captured emission.
type ProgressEntry struct {
	Plugin string
	Text   string
}

// ProgressCollector buffers per-tool-call progress emissions for
// later inclusion in the tool's result envelope. Goroutine-safe so
// concurrent emissions from a wasm plugin land cleanly.
//
// Bounded: at most ProgressCollectorMax entries are retained; later
// emissions overwrite the oldest (ring) so the operator can spam
// progress safely without OOMing the result envelope.
type ProgressCollector struct {
	mu      sync.Mutex
	entries []ProgressEntry
}

// ProgressCollectorMax caps the per-call buffer. 64 entries × ~200
// chars ≈ 13 KiB max — fits comfortably in any tool result envelope.
const ProgressCollectorMax = 64

// Append records one progress emission. Drops the oldest when the
// buffer is at capacity.
func (p *ProgressCollector) Append(plugin, text string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) >= ProgressCollectorMax {
		// Drop the oldest (FIFO).
		copy(p.entries, p.entries[1:])
		p.entries = p.entries[:len(p.entries)-1]
	}
	p.entries = append(p.entries, ProgressEntry{Plugin: plugin, Text: text})
}

// Drain returns and clears the buffered entries. Safe to call once
// the tool has returned — subsequent emissions go nowhere because
// the context (and collector) leave scope.
func (p *ProgressCollector) Drain() []ProgressEntry {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.entries
	p.entries = nil
	return out
}

// ContextWithProgress installs a fresh collector under ctx and returns
// the new context + the collector handle. Callers (Executor.Run)
// install at tool-dispatch entry; Drain at exit; render the entries
// into the result envelope.
func ContextWithProgress(ctx context.Context) (context.Context, *ProgressCollector) {
	pc := &ProgressCollector{}
	return context.WithValue(ctx, progressCtxKey{}, pc), pc
}

// ProgressFromContext retrieves the collector if one was installed.
// Returns nil when no collector is in scope (e.g. direct tool calls
// outside a dispatching loop) — callers should treat nil as "drop."
func ProgressFromContext(ctx context.Context) *ProgressCollector {
	if ctx == nil {
		return nil
	}
	if pc, ok := ctx.Value(progressCtxKey{}).(*ProgressCollector); ok {
		return pc
	}
	return nil
}
