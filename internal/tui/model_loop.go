package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/pkg/agent"
)

// loopState tracks an active /loop session. EP-0036.
type loopState struct {
	prompt   string
	interval time.Duration // 0 = immediate-repeat (fire as soon as idle)
	iter     int           // completed iteration count
}

// loopTickMsg fires when a timed loop interval elapses.
type loopTickMsg struct{}

// loopDoneSignal is the literal string the agent includes in its
// response to self-terminate a loop.
const loopDoneSignal = "[LOOP_DONE]"

// handleLoopCmd processes a /loop slash command. EP-0036.
//
//   /loop stop                  → cancel active loop
//   /loop <prompt>              → immediate-repeat on <prompt>
//   /loop <duration> <prompt>   → timed loop (e.g. /loop 5m check deploy)
func (m *Model) handleLoopCmd(rest string) tea.Cmd {
	rest = strings.TrimSpace(rest)

	if rest == "stop" || rest == "off" || (rest == "" && m.loop != nil) {
		m.loop = nil
		m.appendBlock(block{kind: "system", body: "loop stopped"})
		return nil
	}
	if rest == "" {
		m.appendBlock(block{kind: "system", body: "usage: /loop <prompt>  or  /loop <duration> <prompt>  or  /loop stop"})
		return nil
	}

	// Try to parse a leading duration token.
	var interval time.Duration
	var prompt string
	fields := strings.SplitN(rest, " ", 2)
	if len(fields) == 2 {
		if d, err := time.ParseDuration(fields[0]); err == nil && d > 0 {
			interval = d
			prompt = strings.TrimSpace(fields[1])
		}
	}
	if prompt == "" {
		prompt = rest
	}
	if strings.TrimSpace(prompt) == "" {
		m.appendBlock(block{kind: "system", body: "loop: prompt is required"})
		return nil
	}

	m.loop = &loopState{prompt: prompt, interval: interval}
	if interval > 0 {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("loop started — every %s: %q  (/loop stop to cancel)", interval, prompt)})
	} else {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("loop started — immediate repeat: %q  (/loop stop to cancel)", prompt)})
	}

	// Fire the first iteration immediately.
	return m.loopIterate()
}

// loopIterate queues the next loop prompt if the model is idle.
func (m *Model) loopIterate() tea.Cmd {
	if m.loop == nil {
		return nil
	}
	if m.state == stateStreaming {
		// Busy — the next call comes from model_update when the turn finishes.
		return nil
	}
	m.loop.iter++
	if m.loop.iter > 1 {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("─── loop iteration %d ───", m.loop.iter)})
	}
	// Inject the loop prompt as a user turn and start streaming.
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, m.loop.prompt))
	m.appendBlock(block{kind: "user", body: m.loop.prompt})
	m.renderBlocks()
	return m.startStream()
}

// loopCheckDone scans the agent's latest response for the stop signal.
// Call after each turn. Returns true if the loop was terminated.
func (m *Model) loopCheckDone(responseText string) bool {
	if m.loop == nil {
		return false
	}
	if strings.Contains(responseText, loopDoneSignal) {
		m.loop = nil
		m.appendBlock(block{kind: "system", body: "loop: agent signalled done ([LOOP_DONE])"})
		return true
	}
	return false
}

// loopTick returns a tea.Cmd that fires loopTickMsg after the loop's
// interval. Only used for timed loops (interval > 0).
func (m *Model) loopTick() tea.Cmd {
	if m.loop == nil || m.loop.interval == 0 {
		return nil
	}
	return tea.Tick(m.loop.interval, func(time.Time) tea.Msg {
		return loopTickMsg{}
	})
}

// lastAssistantText returns the most recently accumulated assistant
// response text (m.turnText, which is populated during streaming).
func (m *Model) lastAssistantText() string { return m.turnText }

