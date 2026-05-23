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

// Codex validated finding (post-#46): both cancel helpers must SET
// m.turnCancelled so onToolsExecuted refuses to re-start the provider
// stream when the cancelled tool's `context.Canceled` drains the
// (now-empty) queue. Without this flag the operator-pressed kill
// switch was effectively a no-op: the model just got a "cancelled
// by user" tool result and continued the turn.
func TestCancelRunningStream_SetsTurnCancelled(t *testing.T) {
	cancel, _ := recordCancel()
	m := &Model{streamCancel: cancel}
	if m.turnCancelled {
		t.Fatal("turnCancelled should start false")
	}
	if !m.cancelRunningStream() {
		t.Fatal("cancelRunningStream should report cancellation")
	}
	if !m.turnCancelled {
		t.Error("cancelRunningStream must set m.turnCancelled = true")
	}
}

func TestCancelRunningTool_SetsTurnCancelled(t *testing.T) {
	cancel, _ := recordCancel()
	m := &Model{toolCancel: cancel}
	if m.turnCancelled {
		t.Fatal("turnCancelled should start false")
	}
	if !m.cancelRunningTool() {
		t.Fatal("cancelRunningTool should report cancellation")
	}
	if !m.turnCancelled {
		t.Error("cancelRunningTool must set m.turnCancelled = true")
	}
}

// Load-bearing regression: onToolsExecuted with turnCancelled set must
// NOT call startStream — that's the agent-loop bypass Codex validated.
// The handler should clear the flag, refresh the render, and return
// without dispatching the next provider request (or, if a queued
// prompt is sitting in m.queuedPrompt, promote it instead).
//
// Test asserts indirectly via the returned Cmd shape: when no queued
// prompt + turnCancelled was set, the cmd should be nil (no startStream
// kicked off). Without the gate this test would see a non-nil Cmd —
// the regression vector codex's PoC demonstrated.
func TestOnToolsExecuted_GatedByTurnCancelled(t *testing.T) {
	m := &Model{}
	m.turnCancelled = true
	// No queued prompt; ensure no streamCmd kicked off.
	_, cmd := onToolsExecuted(m, toolsExecutedMsg{results: []agent.ToolResultBlock{
		{ToolUseID: "x", Content: "cancelled by user", IsError: true},
	}})
	if cmd != nil {
		t.Error("onToolsExecuted with turnCancelled set must return nil cmd (no startStream); got non-nil")
	}
	if m.turnCancelled {
		t.Error("onToolsExecuted should clear m.turnCancelled after handling")
	}
	// Conversation-history invariant (Copilot round 1 catch): the
	// tool_result blocks MUST be persisted to m.msgs even on
	// cancellation — the assistant message containing the tool_use
	// blocks was already persisted by onTurnComplete, so leaving the
	// tool_use unpaired produces an invalid history (rejected by
	// OpenAI Chat Completion and others on the next turn). The
	// re-stream is what's suppressed; the history stays well-formed.
	if len(m.msgs) != 1 {
		t.Fatalf("cancelled-turn must persist tool_result blocks to keep history paired; got %d msgs", len(m.msgs))
	}
	if m.msgs[0].Role != agent.RoleTool {
		t.Errorf("persisted msg should be role=tool, got %q", m.msgs[0].Role)
	}
}

// Copilot round 1: clearPendingToolQueue can run when toolCancel is
// already nil (between onToolResult clearing it and advanceToolQueue
// starting the next tool). In that window the cancel-helpers return
// false → turnCancelled doesn't get set → bypass returns. The
// clear-with-pending-work case must set the flag too.
func TestClearPendingToolQueue_SetsTurnCancelledWhenWorkDropped(t *testing.T) {
	m := &Model{pendingCalls: []agent.ToolUseBlock{{ID: "a"}, {ID: "b"}}}
	if m.turnCancelled {
		t.Fatal("turnCancelled should start false")
	}
	n := m.clearPendingToolQueue()
	if n != 2 {
		t.Fatalf("expected 2 cleared, got %d", n)
	}
	if !m.turnCancelled {
		t.Error("clearPendingToolQueue should set turnCancelled when work was dropped (closes the toolCancel=nil/pending-non-empty timing window)")
	}
}

// Inverse: when there was nothing to clear, don't set the flag —
// it'd produce false positives (every Esc, even on idle, would mark
// the next turn cancelled).
func TestClearPendingToolQueue_NoOpDoesNotSetTurnCancelled(t *testing.T) {
	m := &Model{}
	_ = m.clearPendingToolQueue()
	if m.turnCancelled {
		t.Error("clearPendingToolQueue on empty queue must NOT set turnCancelled — would false-positive")
	}
}
