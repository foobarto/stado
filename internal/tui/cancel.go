package tui

// Cancel helpers shared by every keyboard / slash-command path that
// stops in-flight work. Before these existed (Codex #105/#106), the
// Esc/Ctrl+G chord and the /cancel /stop /queue-now /force slash
// commands only consulted m.streamCancel. During the tool-execution
// phase of a turn streamCancel is nil (cleared at streamDoneMsg) but
// m.toolCancel holds the active tool's context — so the operator's
// kill switch silently did nothing for a runaway bash, network, or
// plugin tool, exactly when they needed to use it.

// cancelRunningStream fires m.streamCancel under m.streamMu and
// returns true when something was cancelled. The stream cancel
// function pointer is NOT cleared here — that happens via
// streamDoneMsg in the stream goroutine's cleanup. Idempotent: a
// second call with no in-flight stream returns false without action.
func (m *Model) cancelRunningStream() bool {
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	if m.streamCancel == nil {
		return false
	}
	m.streamCancel()
	return true
}

// cancelRunningTool fires m.toolCancel under m.toolMu, clears the
// pointer, and returns true when a tool was actually cancelled. The
// pointer IS cleared here (unlike streamCancel) because tool
// cancellation completes synchronously from the caller's perspective —
// the goroutine watching the tool's context observes Done and unwinds
// without needing a separate "cancel-was-acked" signal. Idempotent.
//
// Must be called from the TUI event-loop goroutine (the one Update
// runs in) so the lock-acquire order matches the rest of the model;
// background goroutines that need to react to cancellation read
// ctx.Done() instead.
func (m *Model) cancelRunningTool() bool {
	m.toolMu.Lock()
	defer m.toolMu.Unlock()
	if m.toolCancel == nil {
		return false
	}
	m.toolCancel()
	m.toolCancel = nil
	return true
}
