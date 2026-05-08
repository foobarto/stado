package tui

// Lifecycle / ambient message handlers — the dispatcher in
// model_update.go routes per-message-type bubbletea messages here.
// "Lifecycle" covers anything that's neither a streaming/tool event
// nor an input event: window resize, title-bar ticks, async startup
// probe completion, log-tail captures, loop/monitor/background-plugin
// ticks, recovery timeouts, etc.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func onWindowSize(m *Model, msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.slash.Width = msg.Width
	m.layout()
	return m, nil
}

func onTitleTick(m *Model, _ titleTickMsg) (tea.Model, tea.Cmd) {
	// Animated terminal-tab title — see title_spinner.go.
	return m, m.handleTitleTick()
}

func onLogTail(m *Model, msg logTailMsg) (tea.Model, tea.Cmd) {
	m.recordLogLine(msg.line)
	return m, nil
}

func onLocalFallbackReady(m *Model, msg localFallbackReadyMsg) (tea.Model, tea.Cmd) {
	m.providerProbePending = false
	if m.provider == nil && msg.provider != nil {
		m.provider = msg.provider
	}
	if m.providerName == "" && msg.providerName != "" {
		m.providerName = msg.providerName
	}
	if m.model == "" && len(msg.models) > 0 {
		m.model = msg.models[0]
	}
	if msg.provider != nil {
		tuiTrace("startup provider probe resolved",
			"provider", msg.providerName,
			"models", len(msg.models),
			"queued_prompt", m.queuedPrompt != "")
		if m.state == stateIdle && m.queuedPrompt != "" {
			m.renderBlocks()
			return m, m.promoteQueuedPrompt()
		}
		return m, nil
	}
	tuiTrace("startup provider probe found no fallback", "queued_prompt", m.queuedPrompt != "")
	if m.state == stateIdle && m.queuedPrompt != "" {
		queued := m.restoreQueuedPromptToInput()
		m.state = stateError
		m.errorMsg = noProviderConfiguredError().Error()
		m.appendBlock(block{
			kind: "system",
			body: "Provider unavailable: " + noProviderConfiguredError().Error() +
				"\n\nYour draft was restored to the input box: " + trimSeed(queued, 48),
		})
		m.renderBlocks()
	}
	return m, nil
}

func onLoopTick(m *Model, _ loopTickMsg) (tea.Model, tea.Cmd) {
	// EP-0036: timed loop interval elapsed — start next iteration if idle.
	if m.loop != nil && m.state != stateStreaming {
		return m, m.loopIterate()
	}
	// If busy, reschedule — the turn-done path will call loopTick again.
	return m, nil
}

func onMonitorLines(m *Model, msg monitorLinesMsg) (tea.Model, tea.Cmd) {
	// EP-0036: batch of monitor output lines delivered to the session.
	for _, line := range msg {
		m.appendBlock(block{kind: "system", body: "[monitor] " + line})
	}
	m.renderBlocks()
	return m, nil
}

func onMonitorDone(m *Model, msg monitorDoneMsg) (tea.Model, tea.Cmd) {
	// EP-0036: monitored process exited.
	if m.monitor != nil {
		m.monitor = nil
	}
	body := "monitor: process exited"
	if msg.err != nil {
		body += " (" + msg.err.Error() + ")"
	}
	m.appendBlock(block{kind: "system", body: body})
	m.renderBlocks()
	return m, nil
}

func onBackgroundTickResult(m *Model, msg backgroundTickResultMsg) (tea.Model, tea.Cmd) {
	m.backgroundPlugins = msg.survivors
	m.backgroundTickRunning = false
	for _, issue := range msg.issues {
		m.recordBackgroundPluginIssue(issue)
	}
	var cmds []tea.Cmd
	if m.recoveryPluginActive {
		cmds = append(cmds, tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return recoveryTimeoutMsg{} }))
	}
	if m.backgroundTickQueued {
		m.backgroundTickQueued = false
		payload := append([]byte(nil), m.backgroundTickPayload...)
		m.backgroundTickPayload = nil
		cmds = append(cmds, m.tickBackgroundPluginsWithEvent(payload))
	}
	return m, tea.Batch(cmds...)
}

func onRecoveryTimeout(m *Model, _ recoveryTimeoutMsg) (tea.Model, tea.Cmd) {
	if !m.recoveryPluginActive {
		return m, nil
	}
	m.recoveryPluginActive = false
	m.recoveryPluginName = ""
	m.appendBlock(block{
		kind: "system",
		body: "auto-recovery did not produce a compacted child session. Your blocked prompt is still in the editor; use /compact or session fork if you want to recover manually.",
	})
	m.renderBlocks()
	return m, nil
}

func onLocalHint(m *Model, msg localHintMsg) (tea.Model, tea.Cmd) {
	// Async local-runner hint dispatched by ensureProvider's error path.
	// Append as a separate system block so the user sees it arrive after
	// the initial error.
	m.appendBlock(block{kind: "system", body: msg.body})
	m.renderBlocks()
	return m, nil
}

func onSubagentEvent(m *Model, msg subagentEventMsg) (tea.Model, tea.Cmd) {
	m.recordSubagentEvent(msg.ev)
	return m, nil
}
