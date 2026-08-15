package tui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
)

type tuiWorkerBridge struct {
	operations []string
	response   stadoruntime.ApplicationWorkerRun
	err        error
}

func (b *tuiWorkerBridge) CallApplicationController(_ context.Context, operation string, _ []byte) ([]byte, error) {
	b.operations = append(b.operations, operation)
	if b.err != nil {
		return nil, b.err
	}
	return json.Marshal(b.response)
}

func tuiWorkerApplication(run stadoruntime.ApplicationWorkerRun, bridge *tuiWorkerBridge) *stadoruntime.LoadedLifecycleApplication {
	return &stadoruntime.LoadedLifecycleApplication{
		Identity:   plugins.RuntimeIdentity{Namespace: run.PluginID, Canonical: run.PluginID + "@v1"},
		Controller: bridge,
		Application: &pluginruntime.LifecycleApplication{Anchor: pluginruntime.ApplicationAnchor{
			SessionID: run.SessionID, SessionGeneration: run.Generation,
		}},
	}
}

func tuiWorkerRun(status stadoruntime.ApplicationWorkerRunStatus) stadoruntime.ApplicationWorkerRun {
	return stadoruntime.ApplicationWorkerRun{
		SessionID: "session-1", Generation: 3, PluginID: "plugin#quality", RunID: "run-1",
		Version: 1, WALSequence: 42, Objective: "finish the task", Prompt: "continue carefully",
		Conflict: stadoruntime.ApplicationWorkerRunRejectOperatorLoop, Status: status,
	}
}

func TestApplicationCommandConsumesWorkerOnlyAfterSuccessfulCallback(t *testing.T) {
	m := newBudgetModel(t)
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested)
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})

	_, command := onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "quality", application: application,
		result: pluginruntime.CommandResult{Status: "error", WorkerRunID: run.RunID},
	})
	if command != nil {
		t.Fatal("error callback consumed a worker request")
	}
	_, command = onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "quality", application: application,
		result: pluginruntime.CommandResult{Status: "ok", WorkerRunID: run.RunID},
	})
	if command == nil {
		t.Fatal("successful callback did not fetch its worker request")
	}
	if !m.applicationWorkerHandoffRunning {
		t.Fatal("successful callback did not fence the pending native handoff")
	}
	message, ok := command().(applicationWorkerRunLookupMsg)
	if !ok || message.err != nil || message.run.RunID != run.RunID {
		t.Fatalf("lookup message = %#v", message)
	}
}

func TestApplicationCommandConsumesResumeOnlyAfterSuccessfulTypedCallback(t *testing.T) {
	m := newBudgetModel(t)
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunResumeRequested)
	run.Version = 4
	run.TerminalReason = "operator requested review"
	run.TerminalSequence = 40
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})

	_, command := onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "continue", application: application,
		result: pluginruntime.CommandResult{Status: "error", ResumeWorkerRunID: run.RunID},
	})
	if command != nil {
		t.Fatal("error callback consumed a resume request")
	}
	_, command = onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "continue", application: application,
		result: pluginruntime.CommandResult{Status: "ok", ResumeWorkerRunID: run.RunID},
		err:    errors.New("application command trapped"),
	})
	if command != nil {
		t.Fatal("trapped callback consumed a resume request")
	}
	_, command = onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "continue", application: application,
		result: pluginruntime.CommandResult{Status: "ok", ResumeWorkerRunID: run.RunID},
	})
	if command == nil || !m.applicationWorkerHandoffRunning {
		t.Fatalf("resume lookup=%v handoff=%v", command != nil, m.applicationWorkerHandoffRunning)
	}
	message := command().(applicationWorkerRunLookupMsg)
	if message.kind != applicationWorkerHandoffResume || message.run.RunID != run.RunID {
		t.Fatalf("resume lookup message = %+v", message)
	}
}

func TestApplicationCommandCancellationStopsExactLiveRecurrenceAfterBrokerProjection(t *testing.T) {
	m := newBudgetModel(t)
	active := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	active.Version = 2
	cancelled := active
	cancelled.Status = stadoruntime.ApplicationWorkerRunCancelled
	cancelled.Version++
	cancelled.WALSequence++
	cancelled.TerminalReason = "operator cancelled through application"
	bridge := &tuiWorkerBridge{response: cancelled}
	application := tuiWorkerApplication(active, bridge)
	m.loop = &loopState{prompt: active.Prompt, application: application, workerRun: active}
	m.state = stateStreaming
	streamCancelled := false
	m.streamCancel = func() { streamCancelled = true }

	_, lookup := onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "quality", application: application,
		result: pluginruntime.CommandResult{Status: "ok", CancelWorkerRunID: active.RunID},
	})
	if lookup == nil || !m.applicationWorkerHandoffRunning {
		t.Fatalf("cancel lookup=%v handoff=%v", lookup != nil, m.applicationWorkerHandoffRunning)
	}
	message := lookup().(applicationWorkerRunLookupMsg)
	if message.kind != applicationWorkerHandoffCancel || message.run.Status != stadoruntime.ApplicationWorkerRunCancelled {
		t.Fatalf("cancel lookup message = %+v", message)
	}
	onApplicationWorkerRunLookup(m, message)
	if m.loop != nil || !streamCancelled || !m.turnCancelled || m.applicationWorkerHandoffRunning {
		t.Fatalf("cancelled projection loop=%#v stream=%v turn=%v handoff=%v", m.loop, streamCancelled, m.turnCancelled, m.applicationWorkerHandoffRunning)
	}
	if want := []string{"worker.get"}; !reflect.DeepEqual(bridge.operations, want) {
		t.Fatalf("controller operations=%v want=%v", bridge.operations, want)
	}
}

func TestApplicationCommandCancellationFailsClosedOnNonTerminalProjection(t *testing.T) {
	m := newBudgetModel(t)
	active := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	application := tuiWorkerApplication(active, &tuiWorkerBridge{response: active})
	m.loop = &loopState{prompt: active.Prompt, application: application, workerRun: active}
	m.applicationWorkerHandoffRunning = true

	onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{
		application: application, run: active, kind: applicationWorkerHandoffCancel,
	})
	if m.loop == nil || m.applicationFailureSources[applicationFailureWorkerHandoff] == nil || m.applicationWorkerHandoffRunning {
		t.Fatalf("non-terminal cancellation loop=%#v failures=%v handoff=%v", m.loop, m.applicationFailureSources, m.applicationWorkerHandoffRunning)
	}
}

func TestApplicationWorkerResumeUsesDedicatedNativeCASAndRestartsOnce(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationFailure = errors.New("test provider barrier")
	requested := tuiWorkerRun(stadoruntime.ApplicationWorkerRunResumeRequested)
	requested.Version = 4
	requested.TerminalReason = "operator requested review"
	requested.TerminalSequence = 40
	active := requested
	active.Status = stadoruntime.ApplicationWorkerRunActive
	active.Version++
	active.TerminalReason = ""
	active.TerminalSequence = 0
	bridge := &tuiWorkerBridge{response: requested}
	application := tuiWorkerApplication(requested, bridge)

	_, lookup := onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "continue", application: application,
		result: pluginruntime.CommandResult{Status: "ok", ResumeWorkerRunID: requested.RunID},
	})
	lookupMessage := lookup().(applicationWorkerRunLookupMsg)
	_, activate := onApplicationWorkerRunLookup(m, lookupMessage)
	if activate == nil || !m.applicationWorkerHandoffRunning {
		t.Fatalf("resume activation=%v handoff=%v", activate != nil, m.applicationWorkerHandoffRunning)
	}
	bridge.response = active
	activationMessage := activate().(applicationWorkerRunActivationMsg)
	if activationMessage.kind != applicationWorkerHandoffResume {
		t.Fatalf("activation kind=%v", activationMessage.kind)
	}
	onApplicationWorkerRunActivation(m, activationMessage)
	if m.loop == nil || m.loop.workerRun.Status != stadoruntime.ApplicationWorkerRunActive || m.loop.iter != 1 || m.applicationWorkerHandoffRunning {
		t.Fatalf("resumed loop=%#v handoff=%v", m.loop, m.applicationWorkerHandoffRunning)
	}
	if want := []string{"worker.get", "worker.resume.activate"}; !reflect.DeepEqual(bridge.operations, want) {
		t.Fatalf("controller operations=%v want=%v", bridge.operations, want)
	}
	_, replay := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, run: active, kind: applicationWorkerHandoffResume})
	if replay != nil || m.loop.iter != 1 {
		t.Fatalf("resume replay command=%v iter=%d", replay != nil, m.loop.iter)
	}
}

func TestApplicationWorkerResumeConflictPreservesInterruptedWorkflow(t *testing.T) {
	m := newBudgetModel(t)
	m.loop = &loopState{prompt: "existing application work", application: &stadoruntime.LoadedLifecycleApplication{}}
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunResumeRequested)
	run.Version = 4
	bridge := &tuiWorkerBridge{response: run}
	application := tuiWorkerApplication(run, bridge)

	_, command := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{
		application: application, run: run, kind: applicationWorkerHandoffResume,
	})
	if command != nil || m.loop == nil || len(bridge.operations) != 0 || m.applicationFailureSources[applicationFailureWorkerHandoff] == nil {
		t.Fatalf("resume conflict command=%v loop=%#v ops=%v failure=%v", command != nil, m.loop, bridge.operations, m.applicationFailureSources[applicationFailureWorkerHandoff])
	}
}

func TestApplicationWorkerResumeActivationFailurePreservesDraft(t *testing.T) {
	m := newBudgetModel(t)
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunResumeRequested)
	run.Version = 4
	application := tuiWorkerApplication(run, &tuiWorkerBridge{})
	onApplicationWorkerRunActivation(m, applicationWorkerRunActivationMsg{
		application: application, kind: applicationWorkerHandoffResume,
		err: errors.New("pause raced resume request"),
	})
	m.input.SetValue("keep this draft")
	_, command, handled := submitInput(m)
	if !handled || command != nil || m.input.Value() != "keep this draft" || m.applicationFailureSources[applicationFailureWorkerHandoff] == nil {
		t.Fatalf("failed resume handled=%v command=%v draft=%q failure=%v", handled, command != nil, m.input.Value(), m.applicationFailureSources[applicationFailureWorkerHandoff])
	}
}

func TestApplicationWorkerResumeRetainsBrokerHoldBeforeProviderDispatch(t *testing.T) {
	provider := &captureReqProvider{done: make(chan struct{})}
	m := newLifecycleTestModel(t, provider)
	m.broker = tuiScheduleBroker{status: stadoruntime.ScheduleStatus{State: stadoruntime.ScheduleHeld, ReasonCode: "quality-review"}}
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	run.Version = 5
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})

	_, command := onApplicationWorkerRunActivation(m, applicationWorkerRunActivationMsg{
		application: application, run: run, kind: applicationWorkerHandoffResume,
	})
	if command != nil || m.loop == nil || m.loop.workerRun.RunID != run.RunID {
		t.Fatalf("held resume command=%v loop=%#v", command != nil, m.loop)
	}
	select {
	case <-provider.done:
		t.Fatal("provider started while resumed worker remained held")
	default:
	}
	if !hasSystemBlockContaining(m, "quality-review") {
		t.Fatalf("hold reason not surfaced: %+v", m.blocks)
	}
}

func TestApplicationWorkerHandoffFencesOrdinaryInputUntilActivation(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationFailure = errors.New("test provider barrier")
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested)
	active := run
	active.Status = stadoruntime.ApplicationWorkerRunActive
	active.Version++
	bridge := &tuiWorkerBridge{response: run}
	application := tuiWorkerApplication(run, bridge)

	_, lookup := onApplicationCommandResult(m, applicationCommandResultMsg{
		name: "quality", application: application,
		result: pluginruntime.CommandResult{Status: "ok", WorkerRunID: run.RunID},
	})
	if lookup == nil || !m.applicationWorkerHandoffRunning {
		t.Fatalf("lookup=%v handoff=%v", lookup != nil, m.applicationWorkerHandoffRunning)
	}
	m.input.SetValue("must wait for ownership")
	_, ordinary, _ := submitInput(m)
	if ordinary != nil || m.input.Value() != "must wait for ownership" {
		t.Fatalf("ordinary input escaped handoff: command=%v draft=%q", ordinary != nil, m.input.Value())
	}

	lookupMessage := lookup().(applicationWorkerRunLookupMsg)
	_, activate := onApplicationWorkerRunLookup(m, lookupMessage)
	if activate == nil || !m.applicationWorkerHandoffRunning {
		t.Fatalf("activation=%v handoff cleared before CAS=%v", activate != nil, !m.applicationWorkerHandoffRunning)
	}
	bridge.response = active
	activationMessage := activate().(applicationWorkerRunActivationMsg)
	onApplicationWorkerRunActivation(m, activationMessage)
	if m.applicationWorkerHandoffRunning || m.loop == nil || m.loop.workerRun.Status != stadoruntime.ApplicationWorkerRunActive {
		t.Fatalf("handoff=%v loop=%#v", m.applicationWorkerHandoffRunning, m.loop)
	}
}

func TestApplicationWorkerCannotReplaceOperatorLoopWithoutExplicitRule(t *testing.T) {
	m := newBudgetModel(t)
	m.loop = &loopState{prompt: "operator work"}
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested)
	cancelled := run
	cancelled.Status = stadoruntime.ApplicationWorkerRunCancelled
	cancelled.Version++
	bridge := &tuiWorkerBridge{response: cancelled}
	application := tuiWorkerApplication(run, bridge)

	_, command := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, run: run})
	if command == nil {
		t.Fatal("conflicting request was not durably cancelled")
	}
	message := command().(applicationWorkerRunCancellationMsg)
	onApplicationWorkerRunCancellation(m, message)
	if m.loop == nil || m.loop.application != nil || m.loop.prompt != "operator work" {
		t.Fatalf("operator loop was replaced: %#v", m.loop)
	}
	if !reflect.DeepEqual(bridge.operations, []string{"worker.cancel"}) {
		t.Fatalf("operations = %v", bridge.operations)
	}
}

func TestOperatorLoopCannotReplaceApplicationWorkerWithoutDurableStop(t *testing.T) {
	m := newBudgetModel(t)
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})
	m.loop = &loopState{prompt: run.Prompt, application: application, workerRun: run}
	if command := m.handleLoopCmd("different operator work"); command != nil {
		t.Fatal("conflicting operator loop returned a command")
	}
	if m.loop == nil || m.loop.application != application || m.loop.prompt != run.Prompt {
		t.Fatalf("application recurrence was replaced: %#v", m.loop)
	}
}

func TestLookupOfAlreadyActiveWorkerRestoresExactlyOnce(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationFailure = errors.New("test provider barrier")
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})

	_, command := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, run: run})
	if m.loop == nil || m.loop.workerRun.RunID != run.RunID || m.loop.iter != 1 {
		t.Fatalf("active lookup loop=%#v command=%v", m.loop, command != nil)
	}
	_, replay := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, run: run})
	if replay != nil || m.loop.iter != 1 {
		t.Fatalf("active lookup replay command=%v iter=%d", replay != nil, m.loop.iter)
	}
}

func TestWorkerHandoffFailurePreservesOrdinaryDraft(t *testing.T) {
	m := newBudgetModel(t)
	application := tuiWorkerApplication(tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested), &tuiWorkerBridge{})
	onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, err: errors.New("broker unavailable")})
	m.input.SetValue("do not dispatch this")
	beforeMessages := len(m.msgs)
	_, command, handled := submitInput(m)
	if !handled || command != nil || m.input.Value() != "do not dispatch this" || len(m.msgs) != beforeMessages {
		t.Fatalf("handoff failure submit handled=%v command=%v draft=%q messages=%d/%d", handled, command != nil, m.input.Value(), len(m.msgs), beforeMessages)
	}
}

func TestNonActiveWorkerActivationFailsClosedAndPreservesDraft(t *testing.T) {
	m := newBudgetModel(t)
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested)
	application := tuiWorkerApplication(run, &tuiWorkerBridge{})
	onApplicationWorkerRunActivation(m, applicationWorkerRunActivationMsg{application: application, run: run})
	m.input.SetValue("wait for resolved activation")
	_, command, handled := submitInput(m)
	if !handled || command != nil || m.input.Value() != "wait for resolved activation" || m.applicationFailureSources[applicationFailureWorkerHandoff] == nil {
		t.Fatalf("non-active activation handled=%v command=%v draft=%q failure=%v", handled, command != nil, m.input.Value(), m.applicationFailureSources[applicationFailureWorkerHandoff])
	}
}

func TestApplicationWorkerExplicitlyReplacesOperatorLoopExactlyOnce(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationFailure = errors.New("test provider barrier")
	m.loop = &loopState{prompt: "operator work"}
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested)
	run.Conflict = stadoruntime.ApplicationWorkerRunReplaceOperatorLoop
	active := run
	active.Status = stadoruntime.ApplicationWorkerRunActive
	active.Version++
	bridge := &tuiWorkerBridge{response: active}
	application := tuiWorkerApplication(run, bridge)

	_, command := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, run: run})
	message := command().(applicationWorkerRunActivationMsg)
	_, _ = onApplicationWorkerRunActivation(m, message)
	if m.loop == nil || m.loop.application != application || m.loop.workerRun.RunID != run.RunID || m.loop.iter != 1 {
		t.Fatalf("application loop = %#v", m.loop)
	}
	messageCount := len(m.msgs)
	_, replay := onApplicationWorkerRunActivation(m, message)
	if replay != nil || len(m.msgs) != messageCount || m.loop.iter != 1 {
		t.Fatalf("activation replay duplicated recurrence: messages=%d/%d loop=%#v", messageCount, len(m.msgs), m.loop)
	}
}

func TestApplicationWorkerDoesNotReplaceInFlightOperatorIteration(t *testing.T) {
	m := newBudgetModel(t)
	m.state = stateStreaming
	m.loop = &loopState{prompt: "operator work", generation: m.nextLoopGeneration()}
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunRequested)
	run.Conflict = stadoruntime.ApplicationWorkerRunReplaceOperatorLoop
	cancelled := run
	cancelled.Status = stadoruntime.ApplicationWorkerRunCancelled
	cancelled.Version++
	bridge := &tuiWorkerBridge{response: cancelled}
	application := tuiWorkerApplication(run, bridge)

	_, command := onApplicationWorkerRunLookup(m, applicationWorkerRunLookupMsg{application: application, run: run})
	if command == nil {
		t.Fatal("in-flight conflict was not cancelled")
	}
	message := command().(applicationWorkerRunCancellationMsg)
	onApplicationWorkerRunCancellation(m, message)
	if m.loop == nil || m.loop.application != nil || m.loop.prompt != "operator work" {
		t.Fatalf("in-flight operator recurrence was replaced: %#v", m.loop)
	}
}

func TestStaleOperatorTickCannotScheduleReplacementApplicationRun(t *testing.T) {
	m := newBudgetModel(t)
	oldGeneration := m.nextLoopGeneration()
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})
	m.loop = &loopState{
		prompt: run.Prompt, application: application, workerRun: run,
		generation: m.nextLoopGeneration(),
	}
	_, command := onLoopTick(m, loopTickMsg{generation: oldGeneration})
	if command != nil || m.loop.iter != 0 || len(m.msgs) != 0 {
		t.Fatalf("stale tick scheduled replacement run: command=%v loop=%#v messages=%d", command != nil, m.loop, len(m.msgs))
	}
}

func TestApplicationWorkerGateFailureIsDurablyCancelledBeforeLocalClear(t *testing.T) {
	m := newBudgetModel(t)
	m.SetBudgetTokens(100, 200)
	m.cumulativeInputTokens = 250
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	run.Version = 2
	cancelled := run
	cancelled.Status = stadoruntime.ApplicationWorkerRunCancelled
	cancelled.Version++
	bridge := &tuiWorkerBridge{response: cancelled}
	application := tuiWorkerApplication(run, bridge)
	m.loop = &loopState{prompt: run.Prompt, application: application, workerRun: run}

	command := m.loopIterate()
	if command == nil || m.loop == nil || !m.loop.cancelling {
		t.Fatalf("budget gate did not begin durable cancellation: loop=%#v", m.loop)
	}
	message := command().(applicationWorkerRunCancellationMsg)
	onApplicationWorkerRunCancellation(m, message)
	if m.loop != nil || !reflect.DeepEqual(bridge.operations, []string{"worker.cancel"}) {
		t.Fatalf("durable cancellation did not clear recurrence: loop=%#v ops=%v", m.loop, bridge.operations)
	}
}

func TestRecoveredApplicationWorkerRunStartsOnceAndRequiresAdmittedOwner(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationFailure = errors.New("test provider barrier")
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	run.Version = 2
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})
	m.lifecycleApplications = []*stadoruntime.LoadedLifecycleApplication{application}

	_, _ = onApplicationWorkerRunRecovery(m, applicationWorkerRunRecoveryMsg{run: run, found: true})
	if m.loop == nil || m.loop.iter != 1 {
		t.Fatalf("recovered loop = %#v", m.loop)
	}
	messageCount := len(m.msgs)
	_, command := onApplicationWorkerRunRecovery(m, applicationWorkerRunRecoveryMsg{run: run, found: true})
	if command != nil || len(m.msgs) != messageCount || m.loop.iter != 1 {
		t.Fatal("replayed recovery duplicated worker turn")
	}

	missing := newBudgetModel(t)
	onApplicationWorkerRunRecovery(missing, applicationWorkerRunRecoveryMsg{run: run, found: true})
	if missing.applicationFailure == nil || !strings.Contains(missing.applicationFailure.Error(), "no admitted") || missing.loop != nil {
		t.Fatalf("missing application did not fail closed: failure=%v loop=%#v", missing.applicationFailure, missing.loop)
	}
}

func TestRecoveredApplicationWorkerNeverOverwritesOrCancelsDifferentLocalOwner(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationFailure = errors.New("test provider barrier")
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	run.Conflict = stadoruntime.ApplicationWorkerRunReplaceOperatorLoop
	bridge := &tuiWorkerBridge{response: run}
	application := tuiWorkerApplication(run, bridge)
	m.lifecycleApplications = []*stadoruntime.LoadedLifecycleApplication{application}
	m.loop = &loopState{prompt: "local operator recurrence", generation: m.nextLoopGeneration()}
	m.applicationWorkerRecoveryPending = true

	_, command := onApplicationWorkerRunRecovery(m, applicationWorkerRunRecoveryMsg{run: run, found: true})
	if command != nil || m.loop == nil || m.loop.application != nil || m.loop.prompt != "local operator recurrence" {
		t.Fatalf("recovery overwrote local owner: command=%v loop=%#v", command != nil, m.loop)
	}
	if !m.applicationWorkerRecoveryPending || m.applicationFailureSources[applicationFailureWorkerRecovery] == nil {
		t.Fatalf("recovery conflict did not retain fence: pending=%v failures=%v", m.applicationWorkerRecoveryPending, m.applicationFailureSources)
	}
	if len(bridge.operations) != 0 {
		t.Fatalf("recovery implicitly mutated broker worker: %v", bridge.operations)
	}
}

func TestOperatorLoopCannotStartWhileWorkerRecoveryIsUnresolved(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationWorkerRecoveryPending = true
	if command := m.handleLoopCmd("do unrelated recurrence"); command != nil {
		t.Fatal("unresolved recovery started an operator loop command")
	}
	if m.loop != nil {
		t.Fatalf("unresolved recovery installed loop %#v", m.loop)
	}
}

func TestConsumedPauseClearsApplicationLoopLocally(t *testing.T) {
	m := newBudgetModel(t)
	run := tuiWorkerRun(stadoruntime.ApplicationWorkerRunActive)
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})
	m.loop = &loopState{prompt: run.Prompt, application: application, workerRun: run}
	m.state = stateStreaming

	_, command := onApplicationBoundaryResult(m, applicationBoundaryMsg{
		err: &stadoruntime.ScheduleBlockedError{Status: stadoruntime.ScheduleStatus{State: stadoruntime.SchedulePaused}},
	})
	if command != nil || m.loop != nil || m.state != stateIdle {
		t.Fatalf("pause did not end local recurrence: loop=%#v state=%v command=%v", m.loop, m.state, command != nil)
	}
}
