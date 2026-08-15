package tui

// Lifecycle / ambient message handlers — the dispatcher in
// model_update.go routes per-message-type bubbletea messages here.
// "Lifecycle" covers anything that's neither a streaming/tool event
// nor an input event: window resize, title-bar ticks, async startup
// probe completion, log-tail captures, loop/monitor/background-plugin
// ticks, recovery timeouts, etc.

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/runtime"
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

func onDaemonProbeTick(m *Model, _ daemonProbeTickMsg) (tea.Model, tea.Cmd) {
	// Fire a non-blocking daemon-health probe and reschedule the tick —
	// see daemon_health.go.
	return m, tea.Batch(probeDaemonCmd(), daemonProbeTickCmd())
}

func onDaemonHealth(m *Model, msg daemonHealthMsg) (tea.Model, tea.Cmd) {
	m.daemonHealth = msg.state
	return m, nil
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

func onLoopTick(m *Model, msg loopTickMsg) (tea.Model, tea.Cmd) {
	// EP-0036: timed loop interval elapsed — start next iteration if idle.
	if m.loop != nil && msg.generation == m.loop.generation && m.state != stateStreaming {
		return m, m.loopIterate()
	}
	// If busy, reschedule — the turn-done path will call loopTick again.
	return m, nil
}

func onMonitorLine(m *Model, msg monitorLineMsg) (tea.Model, tea.Cmd) {
	// EP-0036: a single live monitor output line delivered to the session.
	// Ignore a line from a superseded monitor instance (a prior /monitor
	// stopped before a new one started): its goroutine can still deliver
	// buffered lines after m.monitor points at the new instance.
	if m.monitor == nil || m.monitor.gen != msg.gen {
		return m, nil
	}
	m.appendBlock(block{kind: "system", body: "[monitor] " + msg.line})
	m.renderBlocks()
	return m, nil
}

func onMonitorDone(m *Model, msg monitorDoneMsg) (tea.Model, tea.Cmd) {
	// EP-0036: monitored process exited. Ignore a stale completion from a
	// superseded monitor instance: after /monitor stop kills monitor A and a
	// new /monitor B starts, A's cancelled goroutine still sends its
	// monitorDoneMsg; a bare nil-check would let it clear B and falsely report
	// B as exited. Matching the generation means only the active monitor's own
	// completion clears state. A nil monitor means /monitor stop already
	// printed "monitor stopped"; don't double-report.
	if m.monitor == nil || m.monitor.gen != msg.gen {
		return m, nil
	}
	m.monitor = nil
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

func onApplicationCommandResult(m *Model, msg applicationCommandResultMsg) (tea.Model, tea.Cmd) {
	m.applicationCommandRunning = false
	body := msg.result.Message
	if msg.err != nil {
		body = msg.err.Error()
	} else if body == "" {
		body = msg.result.Status
	}
	if body == "" {
		body = "completed"
	}
	prefix := msg.name + ": "
	if msg.result.Status == "error" || msg.err != nil {
		prefix += "error: "
	}
	m.appendBlock(block{kind: "system", body: prefix + body})
	m.renderBlocks()
	if msg.err == nil && msg.result.Status == "ok" && msg.result.WorkerRunID != "" && msg.application != nil {
		m.applicationWorkerHandoffRunning = true
		return m, applicationWorkerRunLookupCmd(msg.application, msg.result.WorkerRunID, applicationWorkerHandoffInitial)
	}
	if msg.err == nil && msg.result.Status == "ok" && msg.result.ResumeWorkerRunID != "" && msg.application != nil {
		m.applicationWorkerHandoffRunning = true
		return m, applicationWorkerRunLookupCmd(msg.application, msg.result.ResumeWorkerRunID, applicationWorkerHandoffResume)
	}
	if msg.err == nil && msg.result.Status == "ok" && msg.result.CancelWorkerRunID != "" && msg.application != nil {
		m.applicationWorkerHandoffRunning = true
		return m, applicationWorkerRunLookupCmd(msg.application, msg.result.CancelWorkerRunID, applicationWorkerHandoffCancel)
	}
	return m, nil
}

func onApplicationBoundaryResult(m *Model, msg applicationBoundaryMsg) (tea.Model, tea.Cmd) {
	if msg.generation != 0 && msg.generation != m.applicationVerificationGeneration {
		return m, nil
	}
	if msg.applications != nil {
		survivors := msg.applications[:0:len(msg.applications)]
		for _, application := range msg.applications {
			if application != nil {
				survivors = append(survivors, application)
			}
		}
		m.lifecycleApplications = survivors
	}
	// Publication commits this provider iteration's immutable session anchor
	// before event dispatch and schedule enforcement. A later pause, stop, or
	// callback failure must therefore still consume the local low-word
	// iteration. Otherwise an explicit worker resume would publish a distinct
	// turn at the already-used session_sequence and the application would
	// correctly reject it as a replay.
	if msg.published {
		m.applicationIteration++
	}
	if msg.err != nil {
		if !errors.Is(msg.err, runtime.ErrSchedulePaused) && !errors.Is(msg.err, runtime.ErrScheduleStopped) {
			m.setApplicationFailureSource(applicationFailureTurnBoundary, msg.err)
		} else {
			m.clearApplicationFailureSource(applicationFailureTurnBoundary)
		}
		m.state = stateIdle
		if (errors.Is(msg.err, runtime.ErrSchedulePaused) || errors.Is(msg.err, runtime.ErrScheduleStopped)) && m.loop != nil && m.loop.application != nil {
			// CheckSchedule returns pause/stop only after schedule.consume has
			// durably terminalized this run, so clearing local recurrence here
			// cannot resurrect it after a restart.
			m.loop = nil
		}
		m.appendBlock(block{kind: "system", body: "application turn boundary stopped worker dispatch: " + msg.err.Error()})
		m.renderBlocks()
		if errors.Is(msg.err, runtime.ErrSchedulePaused) || errors.Is(msg.err, runtime.ErrScheduleStopped) {
			return m, m.reconcileApplicationOperatorInput(applicationInputAfterNone)
		}
		return m, nil
	}
	m.clearApplicationFailureSource(applicationFailureTurnBoundary)
	if msg.completed {
		m.closeTurnWithoutTools()
		m.loop = nil
		m.appendBlock(block{kind: "system", body: "lifecycle application completed the worker run"})
		m.renderBlocks()
		return m, m.reconcileApplicationOperatorInput(applicationInputAfterCompletion)
	}
	switch msg.continuation {
	case applicationBoundaryFinishTurn:
		command := m.finishTurnWithoutToolsAfterApplications()
		if m.applicationOwnsOperatorInput() {
			return m, tea.Batch(command, m.reconcileApplicationOperatorInput(applicationInputAfterWorkerTurn))
		}
		return m, command
	case applicationBoundaryContinueTools:
		return m, m.reconcileApplicationOperatorInput(applicationInputAfterToolResults)
	default:
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "application turn boundary returned an unknown continuation"})
		m.renderBlocks()
		return m, nil
	}
}

func onApplicationPollResult(m *Model, msg applicationPollResultMsg) (tea.Model, tea.Cmd) {
	if msg.generation != 0 && msg.generation != m.applicationVerificationGeneration {
		return m, nil
	}
	if msg.generation != 0 && m.applicationPollGeneration != 0 && msg.generation != m.applicationPollGeneration {
		return m, nil
	}
	m.applicationPollRunning = false
	m.applicationPollGeneration = 0
	if msg.applications != nil {
		survivors := msg.applications[:0:len(msg.applications)]
		for _, application := range msg.applications {
			if application != nil {
				survivors = append(survivors, application)
			}
		}
		m.lifecycleApplications = survivors
	}
	if msg.err != nil {
		previous := m.applicationFailureSources[applicationFailureEventPoll]
		if previous == nil || previous.Error() != msg.err.Error() {
			m.recordBackgroundPluginIssue("lifecycle application event dispatcher: " + msg.err.Error())
		}
		m.setApplicationFailureSource(applicationFailureEventPoll, msg.err)
	} else {
		m.clearApplicationFailureSource(applicationFailureEventPoll)
	}
	return m, tea.Batch(applicationPollTickCmd(), m.reconcileApplicationOperatorInput(applicationInputAfterNone), m.continueApplicationWorkerRunRecovery())
}

func onRecoveryTimeout(m *Model, _ recoveryTimeoutMsg) (tea.Model, tea.Cmd) {
	if !m.recoveryPluginActive {
		return m, nil
	}
	recoverApplicationWorker := m.recoveryApplicationWorker
	m.input.SetValue(mergeRecoveryInput(m.recoveryPrompt, m.queuedPrompt, m.input.Value()))
	m.queuedPrompt = ""
	m.recoveryPrompt = ""
	m.recoveryPluginActive = false
	m.recoveryPluginName = ""
	m.recoveryApplicationWorker = false
	m.appendBlock(block{
		kind: "system",
		body: "auto-recovery did not produce a compacted child session. Your blocked prompt is still in the editor; use /compact or session fork if you want to recover manually.",
	})
	m.renderBlocks()
	if recoverApplicationWorker && m.loop != nil && m.loop.application != nil {
		return m, m.cancelApplicationLoop("automatic context recovery did not produce a child session", false)
	}
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
