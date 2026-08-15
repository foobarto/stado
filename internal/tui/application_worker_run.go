package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/runtime"
)

type applicationWorkerHandoffKind uint8

const (
	applicationWorkerHandoffInitial applicationWorkerHandoffKind = iota
	applicationWorkerHandoffResume
	applicationWorkerHandoffCancel
)

type applicationWorkerRunLookupMsg struct {
	application *runtime.LoadedLifecycleApplication
	run         runtime.ApplicationWorkerRun
	kind        applicationWorkerHandoffKind
	err         error
}

type applicationWorkerRunActivationMsg struct {
	application *runtime.LoadedLifecycleApplication
	run         runtime.ApplicationWorkerRun
	kind        applicationWorkerHandoffKind
	err         error
}

type applicationWorkerRunCancellationMsg struct {
	application *runtime.LoadedLifecycleApplication
	run         runtime.ApplicationWorkerRun
	reason      string
	silent      bool
	clearLoop   bool
	err         error
}

type applicationWorkerRunRecoveryMsg struct {
	run   runtime.ApplicationWorkerRun
	found bool
	err   error
}

func applicationWorkerRunLookupCmd(application *runtime.LoadedLifecycleApplication, runID string, kind applicationWorkerHandoffKind) tea.Cmd {
	return func() tea.Msg {
		run, err := application.WorkerRun(context.Background(), runID)
		return applicationWorkerRunLookupMsg{application: application, run: run, kind: kind, err: err}
	}
}

func applicationWorkerRunActivationCmd(application *runtime.LoadedLifecycleApplication, run runtime.ApplicationWorkerRun, kind applicationWorkerHandoffKind) tea.Cmd {
	return func() tea.Msg {
		var active runtime.ApplicationWorkerRun
		var err error
		if kind == applicationWorkerHandoffResume {
			active, err = application.ActivateResumedWorkerRun(context.Background(), run)
		} else {
			active, err = application.ActivateWorkerRun(context.Background(), run)
		}
		return applicationWorkerRunActivationMsg{application: application, run: active, kind: kind, err: err}
	}
}

func applicationWorkerRunCancellationCmd(application *runtime.LoadedLifecycleApplication, run runtime.ApplicationWorkerRun, reason string, clearLoop, silent bool) tea.Cmd {
	return func() tea.Msg {
		cancelled, err := application.CancelWorkerRun(context.Background(), run, reason)
		return applicationWorkerRunCancellationMsg{
			application: application, run: cancelled, reason: reason,
			clearLoop: clearLoop, silent: silent, err: err,
		}
	}
}

func (m *Model) reconcileApplicationWorkerRun() tea.Cmd {
	controller, ok := m.broker.(runtime.ApplicationWorkerRunController)
	if !ok || controller == nil {
		m.applicationWorkerRecoveryPending = false
		return nil
	}
	m.applicationWorkerRecoveryPending = true
	return applicationWorkerRunRecoveryCmd(controller)
}

func (m *Model) continueApplicationWorkerRunRecovery() tea.Cmd {
	if !m.applicationWorkerRecoveryPending {
		return nil
	}
	controller, ok := m.broker.(runtime.ApplicationWorkerRunController)
	if !ok || controller == nil {
		m.setApplicationFailureSource(applicationFailureWorkerRecovery, errors.New("authenticated broker worker-run projection unavailable"))
		return nil
	}
	return applicationWorkerRunRecoveryCmd(controller)
}

func applicationWorkerRunRecoveryCmd(controller runtime.ApplicationWorkerRunController) tea.Cmd {
	return func() tea.Msg {
		run, found, err := controller.ActiveApplicationWorkerRun(context.Background())
		return applicationWorkerRunRecoveryMsg{run: run, found: found, err: err}
	}
}

func (m *Model) lifecycleApplicationForWorkerRun(run runtime.ApplicationWorkerRun) *runtime.LoadedLifecycleApplication {
	for _, application := range m.lifecycleApplications {
		if application != nil && application.Identity.Namespace == run.PluginID && application.Application != nil &&
			application.Application.Anchor.SessionID == run.SessionID && application.Application.Anchor.SessionGeneration == run.Generation {
			return application
		}
	}
	return nil
}

func (m *Model) applicationLoopMatches(application *runtime.LoadedLifecycleApplication, run runtime.ApplicationWorkerRun) bool {
	return m.loop != nil && m.loop.application == application && m.loop.workerRun.RunID == run.RunID &&
		m.loop.workerRun.SessionID == run.SessionID && m.loop.workerRun.Generation == run.Generation
}

func (m *Model) startApplicationWorkerRun(application *runtime.LoadedLifecycleApplication, run runtime.ApplicationWorkerRun) tea.Cmd {
	if application == nil || run.Status != runtime.ApplicationWorkerRunActive {
		return nil
	}
	if m.applicationLoopMatches(application, run) {
		// A replayed callback, rebind, or recovery projection updates the CAS
		// anchor but never starts a second provider turn.
		m.loop.workerRun = run
		return nil
	}
	m.loop = &loopState{prompt: run.Prompt, application: application, workerRun: run, generation: m.nextLoopGeneration()}
	m.applicationToolProjectionGeneration.Add(1)
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("application worker started — %s: %q", run.Objective, run.Prompt)})
	m.renderBlocks()
	if m.applicationInputDeliveryRunning {
		// Startup/rebind recovers input and recurrence concurrently. Install the
		// exact owner now, but let the already-running input projection deliver
		// any ready/recovery records before the first provider recurrence.
		m.rememberApplicationInputAfter(applicationInputAfterWorkerTurn)
		return nil
	}
	return m.loopIterate()
}

func onApplicationWorkerRunLookup(m *Model, msg applicationWorkerRunLookupMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.applicationWorkerHandoffRunning = false
		m.setApplicationFailureSource(applicationFailureWorkerHandoff, fmt.Errorf("application worker lookup: %w", msg.err))
		m.appendBlock(block{kind: "system", body: "application worker request: " + msg.err.Error()})
		m.renderBlocks()
		return m, nil
	}
	m.clearApplicationFailureSource(applicationFailureWorkerHandoff)
	if msg.kind == applicationWorkerHandoffCancel {
		if msg.run.Status != runtime.ApplicationWorkerRunCancelled {
			m.applicationWorkerHandoffRunning = false
			failure := fmt.Errorf("application worker cancellation returned non-cancelled status %s", msg.run.Status)
			m.setApplicationFailureSource(applicationFailureWorkerHandoff, failure)
			m.appendBlock(block{kind: "system", body: failure.Error()})
			m.renderBlocks()
			return m, nil
		}
		if m.loop != nil && !m.applicationLoopMatches(msg.application, msg.run) {
			m.applicationWorkerHandoffRunning = false
			failure := errors.New("cancelled application worker conflicts with a different local recurrence owner")
			m.setApplicationFailureSource(applicationFailureWorkerHandoff, failure)
			m.appendBlock(block{kind: "system", body: failure.Error()})
			m.renderBlocks()
			return m, nil
		}
		if m.loop != nil {
			m.cancelRunningStream()
			m.cancelRunningTool()
			m.clearPendingToolQueue()
			m.loop = nil
			m.applicationToolProjectionGeneration.Add(1)
		}
		m.applicationWorkerHandoffRunning = false
		m.applicationWorkerRecoveryPending = false
		m.clearApplicationFailureSource(applicationFailureWorkerRecovery)
		return m, m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	}
	if msg.run.Status == runtime.ApplicationWorkerRunActive {
		m.applicationWorkerHandoffRunning = false
		if m.applicationLoopMatches(msg.application, msg.run) {
			return m, nil
		}
		if m.loop == nil {
			return m, m.startApplicationWorkerRun(msg.application, msg.run)
		}
		failure := errors.New("durable application worker is active but a different local recurrence owner is installed")
		m.setApplicationFailureSource(applicationFailureWorkerHandoff, failure)
		m.applicationWorkerRecoveryPending = true
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	want := runtime.ApplicationWorkerRunRequested
	if msg.kind == applicationWorkerHandoffResume {
		want = runtime.ApplicationWorkerRunResumeRequested
	}
	if msg.run.Status != want {
		m.applicationWorkerHandoffRunning = false
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("application worker handoff %s is %s; no recurrence started", msg.run.RunID, msg.run.Status)})
		m.renderBlocks()
		return m, nil
	}
	if m.loop != nil {
		if m.loop.application != nil {
			if msg.kind == applicationWorkerHandoffResume {
				return failApplicationWorkerResumeConflict(m, "another application worker run already owns recurrence")
			}
			return m, applicationWorkerRunCancellationCmd(msg.application, msg.run, "another application worker run already owns recurrence", false, false)
		}
		if msg.run.Conflict != runtime.ApplicationWorkerRunReplaceOperatorLoop {
			if msg.kind == applicationWorkerHandoffResume {
				return failApplicationWorkerResumeConflict(m, "operator loop already owns recurrence")
			}
			return m, applicationWorkerRunCancellationCmd(msg.application, msg.run, "operator loop already owns recurrence", false, false)
		}
		if m.state == stateStreaming {
			if msg.kind == applicationWorkerHandoffResume {
				return failApplicationWorkerResumeConflict(m, "operator loop iteration is still in flight")
			}
			return m, applicationWorkerRunCancellationCmd(msg.application, msg.run, "operator loop iteration is still in flight", false, false)
		}
	}
	return m, applicationWorkerRunActivationCmd(msg.application, msg.run, msg.kind)
}

func failApplicationWorkerResumeConflict(m *Model, reason string) (tea.Model, tea.Cmd) {
	m.applicationWorkerHandoffRunning = false
	failure := fmt.Errorf("application worker resume blocked: %s", reason)
	m.setApplicationFailureSource(applicationFailureWorkerHandoff, failure)
	m.appendBlock(block{kind: "system", body: failure.Error()})
	m.renderBlocks()
	return m, nil
}

func onApplicationWorkerRunActivation(m *Model, msg applicationWorkerRunActivationMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.applicationWorkerHandoffRunning = false
		m.setApplicationFailureSource(applicationFailureWorkerHandoff, fmt.Errorf("application worker activation: %w", msg.err))
		m.appendBlock(block{kind: "system", body: "application worker activation: " + msg.err.Error()})
		m.renderBlocks()
		return m, nil
	}
	if msg.run.Status != runtime.ApplicationWorkerRunActive {
		m.applicationWorkerHandoffRunning = false
		failure := fmt.Errorf("application worker activation returned non-active status %s", msg.run.Status)
		m.setApplicationFailureSource(applicationFailureWorkerHandoff, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	if m.loop != nil && !m.applicationLoopMatches(msg.application, msg.run) {
		if m.loop.application != nil || msg.run.Conflict != runtime.ApplicationWorkerRunReplaceOperatorLoop {
			return m, applicationWorkerRunCancellationCmd(msg.application, msg.run, "recurrence owner changed during activation", false, false)
		}
		if m.state == stateStreaming {
			return m, applicationWorkerRunCancellationCmd(msg.application, msg.run, "operator loop iteration started during activation", false, false)
		}
	}
	m.applicationWorkerHandoffRunning = false
	m.clearApplicationFailureSource(applicationFailureWorkerHandoff)
	return m, m.startApplicationWorkerRun(msg.application, msg.run)
}

func onApplicationWorkerRunCancellation(m *Model, msg applicationWorkerRunCancellationMsg) (tea.Model, tea.Cmd) {
	m.applicationWorkerHandoffRunning = false
	if msg.err != nil {
		if msg.clearLoop && m.loop != nil {
			m.loop.cancelling = false
		}
		failure := fmt.Errorf("durably cancel application worker run: %w", msg.err)
		m.setApplicationFailureSource(applicationFailureWorkerHandoff, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	m.clearApplicationFailureSource(applicationFailureWorkerHandoff)
	if msg.clearLoop && m.loop != nil && m.loop.workerRun.RunID == msg.run.RunID && m.loop.workerRun.SessionID == msg.run.SessionID && m.loop.workerRun.Generation == msg.run.Generation {
		m.loop = nil
	}
	if !msg.silent {
		m.appendBlock(block{kind: "system", body: "application worker stopped — " + msg.reason})
		m.renderBlocks()
	}
	if msg.clearLoop {
		return m, m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	}
	return m, nil
}

func onApplicationWorkerRunRecovery(m *Model, msg applicationWorkerRunRecoveryMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		failure := fmt.Errorf("recover durable application worker run: %w", msg.err)
		m.setApplicationFailureSource(applicationFailureWorkerRecovery, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	if !msg.found {
		m.applicationWorkerRecoveryPending = false
		m.clearApplicationFailureSource(applicationFailureWorkerRecovery)
		m.clearApplicationFailureSource(applicationFailureWorkerHandoff)
		return m, m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	}
	application := m.lifecycleApplicationForWorkerRun(msg.run)
	if application == nil {
		failure := errors.New("durable application worker run has no admitted lifecycle application")
		m.setApplicationFailureSource(applicationFailureWorkerRecovery, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	if m.loop != nil && !m.applicationLoopMatches(application, msg.run) {
		// The broker-active run is authoritative, but recovery must never
		// overwrite or cancel a different local owner implicitly. Keep polling
		// behind a persistent fence until the operator safely stops the local
		// recurrence or the broker projection becomes terminal.
		failure := errors.New("durable application worker conflicts with a different local recurrence owner")
		m.setApplicationFailureSource(applicationFailureWorkerRecovery, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	m.applicationWorkerRecoveryPending = false
	m.clearApplicationFailureSource(applicationFailureWorkerRecovery)
	m.clearApplicationFailureSource(applicationFailureWorkerHandoff)
	return m, m.startApplicationWorkerRun(application, msg.run)
}

func (m *Model) cancelApplicationLoop(reason string, silent bool) tea.Cmd {
	if m.loop == nil || m.loop.application == nil {
		return nil
	}
	if m.loop.cancelling {
		return nil
	}
	m.loop.cancelling = true
	return applicationWorkerRunCancellationCmd(m.loop.application, m.loop.workerRun, reason, true, silent)
}
