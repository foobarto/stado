package tui

// Provider streaming + off-band-answer handlers. The streamTick path
// is the hot loop on every reasoning-model turn; throttling lives
// here so bubbletea's renderer stays responsive under high event
// rates. See onStreamTick comments.

import (
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/pkg/agent"
)

func onStreamEvent(m *Model, msg streamEventMsg) (tea.Model, tea.Cmd) {
	m.handleStreamEvent(msg.ev)
	m.renderBlocks()
	return m, nil
}

func onStreamBatch(m *Model, msg streamBatchMsg) (tea.Model, tea.Cmd) {
	for _, ev := range msg.evs {
		m.handleStreamEvent(ev)
	}
	m.renderBlocks()
	return m, nil
}

func onStreamTick(m *Model, _ streamTickMsg) (tea.Model, tea.Cmd) {
	if m.state != stateStreaming {
		return m, nil
	}
	// Drain the shared stream buffer. Throttle the actual
	// renderBlocks() to at most once every 100ms so bubbletea's
	// renderer doesn't choke under reasoning-model event rates.
	// Without this, each tick (50ms) renders the whole viewport
	// — 10+ renders/sec of ANSI-heavy markdown content starves the
	// keyboard reader on bubbletea's unbuffered message channel.
	// Terminal events (seen inside batch) force an immediate render
	// so the final state is never stale.
	m.streamBufMu.Lock()
	batch := m.streamBuf
	m.streamBuf = nil
	closed := m.streamBufClosed
	m.streamBufMu.Unlock()
	boundary := false
	for _, ev := range batch {
		m.handleStreamEvent(ev)
		if ev.Kind == agent.EvDone || ev.Kind == agent.EvError ||
			ev.Kind == agent.EvToolCallStart || ev.Kind == agent.EvToolCallEnd {
			boundary = true
		}
	}
	if len(batch) > 0 && (boundary || time.Since(m.lastStreamRender) > 100*time.Millisecond) {
		m.renderBlocks()
		m.lastStreamRender = time.Now()
	}
	if closed {
		return m, func() tea.Msg { return streamDoneMsg{} }
	}
	return m, streamTickCmd()
}

func onStreamError(m *Model, msg streamErrorMsg) (tea.Model, tea.Cmd) {
	m.streamCancel = nil
	m.streamBufMu.Lock()
	m.streamBufClosed = true
	m.streamBufMu.Unlock()

	// #19: context-overflow errors that arrive synchronously (oaicompat /
	// minimax) auto-recover here; the EvError-event path (Anthropic family)
	// recovers in onStreamDone. See tryContextOverflowRecovery.
	if cmd, ok := m.tryContextOverflowRecovery(msg.err); ok {
		return m, cmd
	}

	m.state = stateError
	m.errorMsg = msg.err.Error()
	m.appendBlock(block{kind: "system", body: "error: " + msg.err.Error()})
	m.renderBlocks()
	return m, nil
}

func onStreamDone(m *Model, _ streamDoneMsg) (tea.Model, tea.Cmd) {
	m.streamCancel = nil
	if m.state == stateError {
		// #19: providers that surface a context overflow as an EvError
		// stream event (Anthropic family) land here in stateError — give
		// the auto-compact backstop a chance before dead-ending.
		if cmd, ok := m.tryContextOverflowRecovery(errors.New(m.errorMsg)); ok {
			return m, cmd
		}
		// EP-0036: an active loop whose iteration errored must stop, not
		// linger. Without this the loop stayed non-nil (status bar shows a
		// dead "↻ loop" forever) but never re-iterated — and blindly
		// re-firing an immediate loop on error would be a no-delay runaway.
		m.stopLoopOnError()
		// The turn is dead — finalize any in-flight thinking/tool block so
		// `auto` mode collapses them. This branch returns before
		// onTurnComplete (the normal finalize site), so without this an
		// errored turn's blocks keep streaming=true forever. Confined to the
		// error path: on the normal path a turn ending with tool calls keeps
		// its tool blocks streaming until onToolResult arrives (they are
		// about to execute), and the no-tool path finalizes in onTurnComplete.
		m.finalizeStreamingBlocks()
		m.renderBlocks()
		return m, nil
	}
	m.maybeEmitBudgetWarning()
	// post_llm fires first (it can rewrite m.turnText), then post_turn sees
	// the final text, then onTurnComplete flushes it into history. This
	// ordering mirrors runtime.AgentLoop, where post_llm precedes the
	// history flush.
	m.firePostLLMHook()
	m.firePostTurnHook()
	var cmds []tea.Cmd
	cmds = append(cmds, m.onTurnComplete(), m.tickBackgroundPluginsWithEvent(m.turnCompleteEvent()))
	// EP-0036: after each turn, check if the loop agent signalled
	// done; if not and loop is active, queue the next iteration or
	// schedule the next tick.
	if m.loop != nil && !m.verifying {
		lastText := m.lastAssistantText()
		if !m.loopCheckDone(lastText) {
			if m.loop.interval > 0 {
				cmds = append(cmds, m.loopTick())
			} else {
				cmds = append(cmds, m.loopIterate())
			}
		}
	}
	return m, tea.Batch(cmds...)
}

func onBtwResult(m *Model, msg btwResultMsg) (tea.Model, tea.Cmd) {
	if msg.errMsg != "" {
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf("btw error: %s", msg.errMsg),
		})
	} else {
		m.appendBlock(block{
			kind: "btw",
			body: msg.reply,
		})
	}
	m.renderBlocks()
	return m, nil
}
