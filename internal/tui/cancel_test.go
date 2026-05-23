package tui

import (
	"context"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// recordCancel returns a (CancelFunc, *bool) pair where the bool flips
// true the first time the func is called. Tests use it to assert a
// cancel function was actually invoked.
func recordCancel() (context.CancelFunc, *bool) {
	called := false
	return func() { called = true }, &called
}

func TestCancelRunningTool_NilCancel_ReturnsFalse(t *testing.T) {
	m := &Model{}
	if m.cancelRunningTool() {
		t.Error("expected false when toolCancel is nil")
	}
	if m.toolCancel != nil {
		t.Error("toolCancel was nil, should remain nil")
	}
}

func TestCancelRunningTool_LiveCancel_FiresAndClears(t *testing.T) {
	cancel, called := recordCancel()
	m := &Model{toolCancel: cancel}

	if !m.cancelRunningTool() {
		t.Error("expected true when toolCancel was non-nil")
	}
	if !*called {
		t.Error("cancel function was not invoked")
	}
	if m.toolCancel != nil {
		t.Error("toolCancel should be cleared after firing")
	}
}

// Idempotent: second call after the first cleared the pointer must
// be a no-op (returns false), not a double-invoke / panic. The
// kill-switch handlers may run more than once if the operator hits
// Esc twice before the cleanup goroutine finishes.
func TestCancelRunningTool_SecondCall_NoOp(t *testing.T) {
	cancel, called := recordCancel()
	m := &Model{toolCancel: cancel}

	_ = m.cancelRunningTool()
	*called = false // reset to detect a (wrong) re-invocation
	if m.cancelRunningTool() {
		t.Error("second call must return false")
	}
	if *called {
		t.Error("cancel function must NOT be invoked twice")
	}
}

func TestCancelRunningStream_NilCancel_ReturnsFalse(t *testing.T) {
	m := &Model{}
	if m.cancelRunningStream() {
		t.Error("expected false when streamCancel is nil")
	}
}

// streamCancel pointer is NOT cleared by cancelRunningStream — the
// stream cleanup goroutine clears it via streamDoneMsg. Document this
// asymmetry vs cancelRunningTool with a test so a future refactor
// doesn't quietly "tidy up" by clearing here.
func TestCancelRunningStream_LiveCancel_FiresButRetainsPointer(t *testing.T) {
	cancel, called := recordCancel()
	m := &Model{streamCancel: cancel}

	if !m.cancelRunningStream() {
		t.Error("expected true when streamCancel was non-nil")
	}
	if !*called {
		t.Error("cancel function was not invoked")
	}
	if m.streamCancel == nil {
		t.Error("streamCancel must NOT be cleared here — that's streamDoneMsg's job")
	}
}

func TestClearPendingToolQueue_EmptyQueue_ReturnsZero(t *testing.T) {
	m := &Model{}
	if n := m.clearPendingToolQueue(); n != 0 {
		t.Errorf("empty queue: expected 0, got %d", n)
	}
}

// Codex P1 regression: a multi-tool turn must abort all pending tools
// when the operator cancels, not just the currently-running one.
// Cancelling A but leaving B + C in m.pendingCalls would let the turn
// keep running through B and C as A's cancellation result triggers
// advanceToolQueue, defeating the "/cancel = stop this turn" semantic.
func TestClearPendingToolQueue_DrainsAllAndReportsCount(t *testing.T) {
	m := &Model{pendingCalls: []agent.ToolUseBlock{
		{ID: "a", Name: "bash"},
		{ID: "b", Name: "fs__read"},
		{ID: "c", Name: "shell__exec"},
	}}
	n := m.clearPendingToolQueue()
	if n != 3 {
		t.Errorf("expected to clear 3 pending calls, got %d", n)
	}
	if len(m.pendingCalls) != 0 {
		t.Errorf("pendingCalls should be empty after clear, got %v", m.pendingCalls)
	}
}

// pendingResults must NOT be cleared — results already collected from
// completed tools in this batch still need to flow to the next
// toolsExecutedMsg so the post-turn cleanup runs and any queued prompt
// dispatches. Defends the asymmetry: queue drops, results carry through.
func TestClearPendingToolQueue_LeavesPendingResultsAlone(t *testing.T) {
	m := &Model{
		pendingCalls: []agent.ToolUseBlock{{ID: "a"}},
		pendingResults: []agent.ToolResultBlock{
			{ToolUseID: "z", Content: "completed before cancel"},
		},
	}
	m.clearPendingToolQueue()
	if len(m.pendingResults) != 1 || m.pendingResults[0].ToolUseID != "z" {
		t.Errorf("clearPendingToolQueue should not touch pendingResults; got %+v", m.pendingResults)
	}
}
