package tui

// In-turn message routing (#16/#17 — see
// .agent/decisions/2026-06-10-steering-queue-interrupt-model.md).
//
// While a turn is in flight a typed message (or an explicit slash arg)
// can be routed three ways:
//
//   - steer     Enter / /steer        inject into the CURRENT turn at the
//                                      next tool boundary (or next turn if
//                                      the turn used no tools).
//   - queue     alt+enter / /queue    defer to the NEXT turn (queuedPrompt).
//   - interrupt ctrl+enter //interrupt cancel the current turn and run now.
//
// applySteer/applyQueue/applyInterrupt are the shared cores called by both
// the key handlers (on the current input text) and the slash commands.

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/pkg/agent"
)

// applySteer routes text into the current turn as steering. While a turn
// streams it's buffered in steeringMsg and injected at the next tool
// boundary; from idle there is nothing to steer, so it falls back to the
// queue path (which, from idle, runs immediately).
func (m *Model) applySteer(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if m.state != stateStreaming {
		return m.applyQueue(text)
	}
	if m.steeringMsg == "" {
		m.steeringMsg = text
	} else {
		m.steeringMsg += "\n" + text // accumulate multiple steers before the boundary
	}
	m.appendBlock(block{kind: "btw", body: "steering — injected at the next tool boundary: " + trimSeed(text, 60)})
	m.renderBlocks()
	return nil
}

// applyQueue defers text to the next turn via the single-slot queuedPrompt
// (re-queuing replaces, with a notice). From idle there's nothing to wait
// for, so it promotes immediately.
func (m *Model) applyQueue(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	replaced := m.queuedPrompt != ""
	if replaced {
		m.clearQueuedUserBlock(true) // drop the prior queued block before re-queuing
	}
	m.queuedPrompt = text
	m.appendBlock(block{kind: "user", body: text, queued: true})
	if m.state == stateStreaming {
		note := "queued — runs when the current turn finishes"
		if replaced {
			note = "queued (replaced the previously queued message) — runs when the current turn finishes"
		}
		m.appendBlock(block{kind: "system", body: note})
		m.renderBlocks()
		return nil
	}
	m.renderBlocks()
	return m.promoteQueuedPrompt()
}

// applyInterrupt cancels the in-flight turn and runs text immediately. With
// no text it fires an already-queued prompt now (the old ForceQueue role).
func (m *Model) applyInterrupt(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text != "" {
		// Queue the message, then cancel — the turn's cleanup drains the
		// queue and dispatches it, so the user's message runs next.
		if m.queuedPrompt != "" {
			m.clearQueuedUserBlock(true)
		}
		m.queuedPrompt = text
		m.appendBlock(block{kind: "user", body: text, queued: true})
	}
	if m.queuedPrompt == "" {
		m.appendBlock(block{kind: "system", body: "interrupt: nothing to run (type a message or queue one first)"})
		m.renderBlocks()
		return nil
	}
	streamCancelled := m.cancelRunningStream()
	toolCancelled := m.cancelRunningTool()
	pendingDropped := m.clearPendingToolQueue()
	if streamCancelled || toolCancelled || pendingDropped > 0 {
		body := "interrupt: cancelled the current turn; running your message now"
		if pendingDropped > 0 {
			body = fmt.Sprintf("%s (%d pending tool(s) dropped)", body, pendingDropped)
		}
		m.appendBlock(block{kind: "system", body: body})
		m.renderBlocks()
		// The cancelled turn's cleanup (onTurnComplete) drains queuedPrompt.
		return nil
	}
	// No in-flight turn to cancel — run the queued prompt directly.
	m.renderBlocks()
	return m.promoteQueuedPrompt()
}

// drainSteering injects a pending steering message into the live
// conversation so the model sees it on its next round-trip. Called after
// the turn's tool results are appended, before the continuation stream.
func (m *Model) drainSteering() {
	if m.steeringMsg == "" {
		return
	}
	steer := m.steeringMsg
	m.steeringMsg = ""
	msg := agent.Text(agent.RoleUser, steer)
	m.msgs = append(m.msgs, msg)
	m.persistMessage(msg)
	m.appendBlock(block{kind: "user", body: steer, source: "steer"})
	tuiTrace("steering injected mid-turn", "chars", len(steer))
}
