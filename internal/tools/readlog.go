package tools

import (
	"context"
	"sync"

	"github.com/foobarto/stado/pkg/tool"
)

// ReadLog is an in-memory, process-local record of reads the read tool has
// surfaced to the model this run. See DESIGN §"Context management" →
// "In-turn deduplication".
//
// Keyed on tool.ReadKey (path + canonical range). Last-writer-wins under
// concurrent RecordRead — "most recent" is defined as call-order, not
// issue-order.
type ReadLog struct {
	mu      sync.Mutex
	entries map[tool.ReadKey]tool.PriorReadInfo

	// turn tracks the 1-indexed turn counter used when recording reads.
	// AgentLoop / the TUI bumps this via BumpTurn at each top-level user
	// prompt. Never zero at record time — tools that call RecordRead before
	// BumpTurn see turn=1.
	turn int
}

// NewReadLog returns an empty read log seeded at turn 1. Process-local;
// not persisted.
func NewReadLog() *ReadLog {
	return &ReadLog{
		entries: make(map[tool.ReadKey]tool.PriorReadInfo),
		turn:    1,
	}
}

// PriorRead returns the most recent prior read for key, or (zero, false)
// when none is recorded.
func (l *ReadLog) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	info, ok := l.entries[key]
	return info, ok
}

// RecordRead stores info under key. When info.Turn is zero (the common
// case — the calling tool doesn't track turn state), the log stamps it
// from its authoritative counter. Callers that already know the turn
// (e.g. replay harnesses) may pass it explicitly.
func (l *ReadLog) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if info.Turn == 0 {
		info.Turn = l.turn
	}
	l.entries[key] = info
}

// Turn returns the current turn counter.
func (l *ReadLog) Turn() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turn
}

// BumpTurn advances the counter. Callers fire this on each top-level user
// prompt — not on agent-internal re-streams after tool execution.
func (l *ReadLog) BumpTurn() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turn++
}

// NullHost is a zero-behaviour tool.Host suitable for tests. PriorRead
// always returns (zero, false); RecordRead is a no-op; Approve always
// allows. Embed it in test doubles so tests don't reimplement boilerplate
// every time the Host interface grows.
type NullHost struct{}

func (NullHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (NullHost) Workdir() string                                        { return "" }
func (NullHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool)      { return tool.PriorReadInfo{}, false }
func (NullHost) RecordRead(tool.ReadKey, tool.PriorReadInfo)            {}

// Compile-time assertion that NullHost satisfies the interface.
var _ tool.Host = NullHost{}
