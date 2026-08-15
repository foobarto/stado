package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

// loopState tracks an active /loop session. EP-0036.
type loopState struct {
	prompt      string
	interval    time.Duration // 0 = immediate-repeat (fire as soon as idle)
	iter        int           // completed iteration count
	application *runtime.LoadedLifecycleApplication
	workerRun   runtime.ApplicationWorkerRun
	cancelling  bool
	generation  uint64
}

// loopActive reports whether a /loop session is currently running — drives
// the status-bar "↻ loop" indicator. EP-0036.
func (m *Model) loopActive() bool { return m.loop != nil }

// loopIntervalLabel returns the parenthesised interval suffix for the
// status-bar loop indicator ("(5m)" for a timed loop), or "" for an
// immediate-repeat loop or no loop. EP-0036.
func (m *Model) loopIntervalLabel() string {
	if m.loop == nil || m.loop.interval <= 0 {
		return ""
	}
	return "(" + compactDuration(m.loop.interval) + ")"
}

// compactDuration renders a loop interval compactly: "5m", "30s", "1m30s",
// "2h", "2h30m". Whole units drop the lower component.
func compactDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		if d%time.Hour == 0 {
			return fmt.Sprintf("%dh", int(d/time.Hour))
		}
		return fmt.Sprintf("%dh%dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	case d >= time.Minute:
		if d%time.Minute == 0 {
			return fmt.Sprintf("%dm", int(d/time.Minute))
		}
		return fmt.Sprintf("%dm%ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// loopTickMsg fires when a timed loop interval elapses.
type loopTickMsg struct{ generation uint64 }

func (m *Model) nextLoopGeneration() uint64 {
	m.loopGeneration++
	return m.loopGeneration
}

// loopDoneSignal is the literal string the agent includes in its
// response to self-terminate a loop.
const loopDoneSignal = "[LOOP_DONE]"

// handleLoopCmd processes a /loop slash command. EP-0036.
//
//	/loop stop                  → cancel active loop
//	/loop <prompt>              → immediate-repeat on <prompt>
//	/loop <duration> <prompt>   → timed loop (e.g. /loop 5m check deploy)
func (m *Model) handleLoopCmd(rest string) tea.Cmd {
	rest = strings.TrimSpace(rest)

	if rest == "stop" || rest == "off" || (rest == "" && m.loop != nil) {
		if m.loop == nil {
			// Mirror /monitor stop: don't claim to have stopped a loop
			// that was never running.
			m.appendBlock(block{kind: "system", body: "no active loop"})
			return nil
		}
		if m.loop.application != nil {
			return m.cancelApplicationLoop("operator stopped recurrence with /loop stop", false)
		}
		m.loop = nil
		m.appendBlock(block{kind: "system", body: "loop stopped"})
		return nil
	}
	if rest == "" {
		m.appendBlock(block{kind: "system", body: "usage: /loop <prompt>  or  /loop <duration> <prompt>  or  /loop stop"})
		return nil
	}
	if m.applicationWorkerRecoveryPending || m.applicationFailureSources[applicationFailureWorkerRecovery] != nil || m.applicationFailureSources[applicationFailureWorkerHandoff] != nil {
		m.appendBlock(block{kind: "system", body: "loop: durable application worker ownership is unresolved; wait for recovery or use /loop stop on an existing local recurrence"})
		return nil
	}
	if m.loop != nil && m.loop.application != nil {
		m.appendBlock(block{kind: "system", body: "loop: application worker owns recurrence; use /loop stop before starting an operator loop"})
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

	m.loop = &loopState{prompt: prompt, interval: interval, generation: m.nextLoopGeneration()}
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
	// A /loop runs unattended, so each iteration must respect the same budget
	// hard-cap and context hard-threshold gates a manual Enter does
	// (submitInput) — otherwise a timed/immediate loop spins past
	// a hard token budget or the context bound with no human present to
	// `/budget ack` or `/compact`. The manual recovery flows need interaction a
	// loop can't supply, and silently skipping would busy-spin an immediate
	// loop, so the safe move is to STOP the loop and say why (same shape as
	// stopLoopOnError).
	if m.budgetExceeded() {
		breach, knob := m.budgetBreachDescription()
		if m.loop.application != nil {
			return m.cancelApplicationLoop(fmt.Sprintf("%s; budget.%s requires operator recovery", breach, knob), false)
		}
		m.loop = nil
		m.appendBlock(block{kind: "system", body: fmt.Sprintf(
			"loop stopped — %s. Raise [budget].%s or run /budget ack, then /loop to restart.",
			breach, knob)})
		m.renderBlocks()
		return nil
	}
	if m.aboveHardThreshold() {
		if m.loop.application != nil {
			return m.cancelApplicationLoop(fmt.Sprintf("context at %.0f%% reached hard threshold %.0f%%", 100*m.contextFraction(), 100*m.ctxHardThreshold), false)
		}
		m.loop = nil
		m.appendBlock(block{kind: "system", body: fmt.Sprintf(
			"loop stopped — context at %.0f%% (hard threshold %.0f%%). /compact or fork, then /loop to restart.",
			100*m.contextFraction(), 100*m.ctxHardThreshold)})
		m.renderBlocks()
		return nil
	}
	m.loop.iter++
	if m.loop.iter > 1 {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("─── loop iteration %d ───", m.loop.iter)})
	}
	if err := m.setBrokerTaint(runtime.ContextClean); err != nil {
		if m.loop.application != nil {
			return m.cancelApplicationLoop("broker taint reset failed: "+err.Error(), false)
		}
		m.loop = nil
		m.appendBlock(block{kind: "system", body: "loop stopped - broker taint reset failed: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	// Inject the loop prompt as a user turn and start streaming.
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, m.loop.prompt))
	m.appendBlock(block{kind: "user", body: m.loop.prompt})
	m.renderBlocks()
	return m.startStream()
}

// stopLoopOnError cancels an active loop after one of its iterations ended
// in stateError and emits a system block so the user knows the loop stopped
// and why. A no-op when no loop is running. Re-iterating instead would be
// wrong: an immediate-repeat loop would spin error→re-fire→error with no
// delay, and a timed loop would silently never reschedule its tick while the
// status bar still claimed it was active.
func (m *Model) stopLoopOnError() tea.Cmd {
	if m.loop == nil {
		return nil
	}
	if m.loop.application != nil {
		return m.cancelApplicationLoop("last worker iteration errored", false)
	}
	m.loop = nil
	m.appendBlock(block{kind: "system", body: "loop stopped — the last iteration errored (use /loop to restart once the issue is resolved)"})
	m.renderBlocks()
	return nil
}

// stopBackgroundActivity cancels any running /loop and /monitor without
// emitting blocks. /clear wipes the conversation, so it must also halt the
// background activity that was driving it — otherwise the loop's ↻ indicator
// points at a wiped context and the monitor goroutine keeps streaming into a
// cleared screen (orphaned). Silent by design: /clear blanks the block list
// right after, so any "stopped" notice would be wiped anyway.
func (m *Model) stopBackgroundActivity() tea.Cmd {
	var command tea.Cmd
	if m.loop != nil && m.loop.application != nil {
		command = m.cancelApplicationLoop("conversation cleared by operator", true)
	} else {
		m.loop = nil
	}
	if m.monitor != nil {
		m.monitor.cancel()
		m.monitor = nil
	}
	return command
}

// loopCheckDone scans the agent's latest response for the stop signal.
// Call after each turn. Returns true if the loop was terminated.
func (m *Model) loopCheckDone(responseText string) bool {
	if m.loop == nil {
		return false
	}
	if m.loop.application != nil {
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
	generation := m.loop.generation
	return tea.Tick(m.loop.interval, func(time.Time) tea.Msg {
		return loopTickMsg{generation: generation}
	})
}

// lastAssistantText returns the most recently accumulated assistant
// response text (m.turnText, which is populated during streaming).
func (m *Model) lastAssistantText() string { return m.turnText }
