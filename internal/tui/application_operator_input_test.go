package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

type tuiApplicationInputBroker struct {
	captured          []string
	captureRequestIDs []string
	captureErr        error
	state             runtime.ApplicationInputState
	inputCommitErr    error
	inputCommits      int
	continuations     int
}

type tuiRecoveringInputBroker struct {
	*tuiApplicationInputBroker
	run   runtime.ApplicationWorkerRun
	found bool
	err   error
}

func (b *tuiRecoveringInputBroker) ActiveApplicationWorkerRun(context.Context) (runtime.ApplicationWorkerRun, bool, error) {
	return b.run, b.found, b.err
}

type tuiRecoveryTransitionRoot struct {
	runs map[string]runtime.ApplicationWorkerRun
}

func (r *tuiRecoveryTransitionRoot) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return r, nil
}
func (r *tuiRecoveryTransitionRoot) SetTaint(context.Context, runtime.ContextTaint) error {
	return nil
}
func (r *tuiRecoveryTransitionRoot) Sandbox() runtime.ExecutorSandbox {
	return runtime.ExecutorSandbox{}
}
func (r *tuiRecoveryTransitionRoot) Worktree() string { return "" }
func (r *tuiRecoveryTransitionRoot) Close() error     { return nil }
func (r *tuiRecoveryTransitionRoot) OpenLogicalSession(_ context.Context, _, subject string) (runtime.BrokerController, error) {
	run, found := r.runs[subject]
	return &tuiRecoveringInputBroker{tuiApplicationInputBroker: &tuiApplicationInputBroker{}, run: run, found: found}, nil
}

func (b *tuiApplicationInputBroker) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return b, nil
}
func (b *tuiApplicationInputBroker) SetTaint(context.Context, runtime.ContextTaint) error {
	return nil
}
func (b *tuiApplicationInputBroker) Sandbox() runtime.ExecutorSandbox {
	return runtime.ExecutorSandbox{}
}
func (b *tuiApplicationInputBroker) Worktree() string { return "" }
func (b *tuiApplicationInputBroker) Close() error     { return nil }
func (b *tuiApplicationInputBroker) CaptureApplicationOperatorInput(_ context.Context, text, requestID string) (runtime.ApplicationOperatorInput, error) {
	b.captured = append(b.captured, text)
	b.captureRequestIDs = append(b.captureRequestIDs, requestID)
	if b.captureErr != nil {
		return runtime.ApplicationOperatorInput{}, b.captureErr
	}
	return runtime.ApplicationOperatorInput{ID: "operator_input_1", Text: text, Digest: receiverDigest(text), Status: "queued", Version: 1}, nil
}
func (b *tuiApplicationInputBroker) ApplicationOperatorInputState(context.Context) (runtime.ApplicationInputState, error) {
	return b.state, nil
}
func (b *tuiApplicationInputBroker) CommitApplicationOperatorInput(_ context.Context, input runtime.ApplicationOperatorInput, recovery bool) (runtime.ApplicationOperatorInput, error) {
	b.inputCommits++
	if b.inputCommitErr != nil {
		err := b.inputCommitErr
		b.inputCommitErr = nil
		return runtime.ApplicationOperatorInput{}, err
	}
	input.Status, input.Recovered = "delivered", recovery
	b.state.ReadyInputs = nil
	b.state.RecoveryInputs = nil
	return input, nil
}
func (b *tuiApplicationInputBroker) CommitApplicationContinuation(_ context.Context, input runtime.ApplicationContinuation) (runtime.ApplicationContinuation, error) {
	b.continuations++
	input.Status = "delivered"
	b.state.PendingContinuation = nil
	return input, nil
}

func receiverDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func applicationInputTestModel(t *testing.T, broker *tuiApplicationInputBroker) *Model {
	t.Helper()
	m := newBudgetModel(t)
	m.broker = broker
	m.session = &stadogit.Session{ID: "git-session", WorktreePath: t.TempDir()}
	m.loop = &loopState{application: &runtime.LoadedLifecycleApplication{}, workerRun: runtime.ApplicationWorkerRun{RunID: "run-1", Status: runtime.ApplicationWorkerRunActive}}
	return m
}

func TestApplicationOwnedInputCaptureNeverFallsThroughAndRestoresOnFailure(t *testing.T) {
	broker := &tuiApplicationInputBroker{}
	m := applicationInputTestModel(t, broker)
	m.state = stateStreaming
	m.input.SetValue("a separate request")
	_, command, handled := submitInput(m)
	if !handled || command == nil || m.input.Value() != "" || m.steeringMsg != "" {
		t.Fatalf("capture submit handled=%v command=%v input=%q steer=%q", handled, command, m.input.Value(), m.steeringMsg)
	}
	message := command().(applicationInputCaptureMsg)
	onApplicationInputCapture(m, message)
	if len(broker.captured) != 1 || broker.captured[0] != "a separate request" || len(m.blocks) < 2 || !m.blocks[len(m.blocks)-2].queued {
		t.Fatalf("capture=%v blocks=%#v", broker.captured, m.blocks)
	}

	failedBroker := &tuiApplicationInputBroker{captureErr: errors.New("backpressure")}
	failed := applicationInputTestModel(t, failedBroker)
	failed.input.SetValue("must not disappear")
	_, command, _ = submitInput(failed)
	message = command().(applicationInputCaptureMsg)
	onApplicationInputCapture(failed, message)
	if failed.input.Value() != "must not disappear" || failed.queuedPrompt != "" || failed.steeringMsg != "" || !strings.Contains(failed.blocks[len(failed.blocks)-1].body, "not submitted unsupervised") {
		t.Fatalf("failed capture input=%q queued=%q steer=%q blocks=%#v", failed.input.Value(), failed.queuedPrompt, failed.steeringMsg, failed.blocks)
	}
	failedBroker.captureErr = nil
	_, retryCommand, _ := submitInput(failed)
	retryMessage := retryCommand().(applicationInputCaptureMsg)
	onApplicationInputCapture(failed, retryMessage)
	if len(failedBroker.captureRequestIDs) != 2 || failedBroker.captureRequestIDs[0] != failedBroker.captureRequestIDs[1] || failed.applicationFailure != nil {
		t.Fatalf("capture retry ids=%v failure=%v", failedBroker.captureRequestIDs, failed.applicationFailure)
	}
}

func TestApplicationWorkerRecoveryGatePreservesDraftUntilExactOwnerRestored(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	base := &tuiApplicationInputBroker{}
	broker := &tuiRecoveringInputBroker{tuiApplicationInputBroker: base, run: run, found: true}
	m := newBudgetModel(t)
	m.broker = broker
	m.session = &stadogit.Session{ID: "git-session", WorktreePath: t.TempDir()}
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{tuiWorkerApplication(run, &tuiWorkerBridge{response: run})}
	m.applicationFailure = errors.New("test provider barrier")

	recovery := m.reconcileApplicationWorkerRun()
	if recovery == nil || !m.applicationWorkerRecoveryPending {
		t.Fatalf("recovery command=%v pending=%v", recovery != nil, m.applicationWorkerRecoveryPending)
	}
	m.input.SetValue("must wait for exact owner")
	beforeMessages := len(m.msgs)
	_, command, handled := submitInput(m)
	if !handled || command != nil || m.input.Value() != "must wait for exact owner" || len(m.msgs) != beforeMessages || len(base.captured) != 0 {
		t.Fatalf("pending submit handled=%v command=%v draft=%q msgs=%d/%d captures=%v", handled, command != nil, m.input.Value(), len(m.msgs), beforeMessages, base.captured)
	}
	message := recovery().(applicationWorkerRunRecoveryMsg)
	_, first := onApplicationWorkerRunRecovery(m, message)
	if m.applicationWorkerRecoveryPending || m.loop == nil || m.loop.workerRun.RunID != run.RunID || m.loop.iter != 1 {
		t.Fatalf("recovered loop=%#v pending=%v command=%v", m.loop, m.applicationWorkerRecoveryPending, first != nil)
	}
	_, replay := onApplicationWorkerRunRecovery(m, message)
	if replay != nil || m.loop.iter != 1 {
		t.Fatalf("replayed recovery command=%v iter=%d", replay != nil, m.loop.iter)
	}
}

func TestRecoveredWorkerWaitsForReadyInputDeliveryBeforeFirstRecurrence(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	text := "ready before restart"
	input := runtime.ApplicationOperatorInput{ID: "operator_input_ready", Text: text, Digest: receiverDigest(text), Status: "ready", Version: 2}
	base := &tuiApplicationInputBroker{state: runtime.ApplicationInputState{ActiveWorkerRun: &run, ReadyInputs: []runtime.ApplicationOperatorInput{input}}}
	broker := &tuiRecoveringInputBroker{tuiApplicationInputBroker: base, run: run, found: true}
	m := newBudgetModel(t)
	m.broker = broker
	m.session = &stadogit.Session{ID: "git-session", WorktreePath: t.TempDir()}
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{tuiWorkerApplication(run, &tuiWorkerBridge{response: run})}
	m.applicationFailure = errors.New("test provider barrier")

	inputState := m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	recovery := m.reconcileApplicationWorkerRun()
	workerMessage := recovery().(applicationWorkerRunRecoveryMsg)
	_, workerCommand := onApplicationWorkerRunRecovery(m, workerMessage)
	if workerCommand != nil || m.loop == nil || m.loop.iter != 0 {
		t.Fatalf("recovered worker raced input delivery: command=%v loop=%#v", workerCommand != nil, m.loop)
	}
	stateMessage := inputState().(applicationInputStateMsg)
	_, delivery := onApplicationInputState(m, stateMessage)
	deliveryMessage := delivery().(applicationInputDeliveryMsg)
	_, nextState := onApplicationInputDelivery(m, deliveryMessage)
	stateMessage = nextState().(applicationInputStateMsg)
	_, resumed := onApplicationInputState(m, stateMessage)
	if resumed != nil || m.loop.iter != 1 || len(m.msgs) != 2 || latestUserPrompt(m.msgs[:1]) != text || latestUserPrompt(m.msgs) != run.Prompt {
		t.Fatalf("post-delivery recurrence command=%v iter=%d messages=%#v", resumed != nil, m.loop.iter, m.msgs)
	}
}

func TestSessionSwitchAwayAndBackRecoversExactApplicationWorkerOnce(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	run.SessionID = ids.first
	root := &tuiRecoveryTransitionRoot{runs: map[string]runtime.ApplicationWorkerRun{ids.first: run}}
	m.brokerRoot, m.broker = root, root
	application := tuiWorkerApplication(run, &tuiWorkerBridge{response: run})
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{application}
	m.loop = &loopState{prompt: run.Prompt, application: application, workerRun: run}

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if !m.applicationWorkerRecoveryPending {
		t.Fatal("session B did not enter fail-closed worker recovery")
	}
	command := m.continueApplicationWorkerRunRecovery()
	message := command().(applicationWorkerRunRecoveryMsg)
	_, inputStateCommand := onApplicationWorkerRunRecovery(m, message)
	if m.applicationWorkerRecoveryPending || m.loop != nil {
		t.Fatalf("session B recovery pending=%v loop=%#v", m.applicationWorkerRecoveryPending, m.loop)
	}
	inputStateMessage := inputStateCommand().(applicationInputStateMsg)
	onApplicationInputState(m, inputStateMessage)

	if err := m.switchToSession(ids.first); err != nil {
		t.Fatal(err)
	}
	application = tuiWorkerApplication(run, &tuiWorkerBridge{response: run})
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{application}
	m.applicationFailure = errors.New("test provider barrier")
	m.input.SetValue("hold while session A recovers")
	_, submitted, handled := submitInput(m)
	if !handled || submitted != nil || m.input.Value() != "hold while session A recovers" || len(m.msgs) != 0 {
		t.Fatalf("session A recovery submit handled=%v command=%v draft=%q msgs=%d", handled, submitted != nil, m.input.Value(), len(m.msgs))
	}
	command = m.continueApplicationWorkerRunRecovery()
	message = command().(applicationWorkerRunRecoveryMsg)
	onApplicationWorkerRunRecovery(m, message)
	if m.applicationWorkerRecoveryPending || m.loop == nil || m.loop.workerRun.SessionID != ids.first || m.loop.iter != 1 {
		t.Fatalf("session A recovered loop=%#v pending=%v", m.loop, m.applicationWorkerRecoveryPending)
	}
	onApplicationWorkerRunRecovery(m, message)
	if m.loop.iter != 1 {
		t.Fatalf("session A duplicate recurrence iter=%d", m.loop.iter)
	}
}

func TestCancellingApplicationWorkerStillOwnsInputUntilDurableCAS(t *testing.T) {
	broker := &tuiApplicationInputBroker{}
	m := applicationInputTestModel(t, broker)
	m.loop.cancelling = true
	m.input.SetValue("arrived during cancellation")
	beforeMessages := len(m.msgs)
	_, command, handled := submitInput(m)
	if !handled || command == nil || len(m.msgs) != beforeMessages {
		t.Fatalf("cancelling submit handled=%v command=%v messages=%d/%d", handled, command != nil, len(m.msgs), beforeMessages)
	}
	message := command().(applicationInputCaptureMsg)
	onApplicationInputCapture(m, message)
	if len(broker.captured) != 1 || broker.captured[0] != "arrived during cancellation" {
		t.Fatalf("cancelling capture=%v", broker.captured)
	}
}

func TestApplicationInputDeliveryCrashBetweenConversationAndBrokerCommitDoesNotDuplicate(t *testing.T) {
	text := "related correction"
	input := runtime.ApplicationOperatorInput{ID: "operator_input_1", Text: text, Digest: receiverDigest(text), Status: "ready", Version: 2}
	broker := &tuiApplicationInputBroker{state: runtime.ApplicationInputState{ReadyInputs: []runtime.ApplicationOperatorInput{input}}, inputCommitErr: errors.New("broker unavailable")}
	m := applicationInputTestModel(t, broker)
	m.state = stateIdle
	m.provider = uatStub{}
	m.blocks = append(m.blocks, block{kind: "user", body: text, queued: true, source: "application-input", deliveryID: input.ID})

	stateCommand := m.reconcileApplicationOperatorInput(applicationInputAfterWorkerTurn)
	stateMessage := stateCommand().(applicationInputStateMsg)
	_, deliveryCommand := onApplicationInputState(m, stateMessage)
	deliveryMessage := deliveryCommand().(applicationInputDeliveryMsg)
	if !deliveryMessage.appended || deliveryMessage.err == nil {
		t.Fatalf("first delivery = %#v", deliveryMessage)
	}
	onApplicationInputDelivery(m, deliveryMessage)
	if len(m.msgs) != 1 {
		t.Fatalf("live receiver messages after commit failure = %#v", m.msgs)
	}

	// The ordinary periodic retry carries no boundary hint; the failed delivery
	// must retain its prior worker-turn continuation durably in model state.
	stateCommand = m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	stateMessage = stateCommand().(applicationInputStateMsg)
	_, deliveryCommand = onApplicationInputState(m, stateMessage)
	deliveryMessage = deliveryCommand().(applicationInputDeliveryMsg)
	if deliveryMessage.appended || deliveryMessage.err != nil {
		t.Fatalf("retry delivery = %#v", deliveryMessage)
	}
	_, followup := onApplicationInputDelivery(m, deliveryMessage)
	loaded, err := runtime.LoadConversation(m.session.WorktreePath)
	if err != nil || len(loaded) != 1 || len(m.msgs) != 1 || broker.inputCommits != 2 || followup == nil || m.applicationFailure != nil {
		t.Fatalf("loaded=%#v live=%#v commits=%d err=%v", loaded, m.msgs, broker.inputCommits, err)
	}
	stateMessage = followup().(applicationInputStateMsg)
	_, resumed := onApplicationInputState(m, stateMessage)
	if resumed == nil || m.loop.iter != 1 {
		t.Fatalf("worker did not resume after durable retry: command=%v iter=%d", resumed != nil, m.loop.iter)
	}
}

func TestApplicationInputRecoveryDoesNotClearUnrelatedFailureAndSurvivesReload(t *testing.T) {
	m := newBudgetModel(t)
	inputFailure := errors.New("input state unavailable")
	m.setApplicationFailureSource(applicationFailureInputState, inputFailure)
	m.rebindLifecycleApplications(context.Background(), nil)
	if m.applicationFailure != inputFailure {
		t.Fatalf("reload cleared input failure: %v", m.applicationFailure)
	}
	unrelated := errors.New("unrelated lifecycle failure")
	m.setApplicationFailureSource("future:independent", unrelated)
	m.setApplicationFailureSource(applicationFailureInputState, inputFailure)
	m.clearApplicationFailureSource(applicationFailureInputState)
	if m.applicationFailure != unrelated {
		t.Fatalf("input recovery cleared unrelated failure: %v", m.applicationFailure)
	}
}

func TestApplicationInputStateRetryRetainsWorkerBoundary(t *testing.T) {
	m := applicationInputTestModel(t, &tuiApplicationInputBroker{})
	barrier := errors.New("test provider barrier")
	m.setApplicationFailureSource("test:provider-barrier", barrier)
	m.applicationInputDeliveryRunning = true
	_, command := onApplicationInputState(m, applicationInputStateMsg{after: applicationInputAfterWorkerTurn, err: errors.New("state unavailable")})
	if command != nil || m.applicationInputPendingAfter != applicationInputAfterWorkerTurn {
		t.Fatalf("failed state lost worker boundary: command=%v pending=%v", command != nil, m.applicationInputPendingAfter)
	}
	command = m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	message := command().(applicationInputStateMsg)
	_, resumed := onApplicationInputState(m, message)
	if resumed != nil || m.loop.iter != 1 || m.applicationFailure != barrier || m.applicationFailureSources[applicationFailureInputState] != nil {
		t.Fatalf("state retry resumed=%v iter=%d failure=%v state-source=%v", resumed != nil, m.loop.iter, m.applicationFailure, m.applicationFailureSources[applicationFailureInputState])
	}
}

func TestReviewingApplicationInputRetainsWorkerBoundaryUntilExactRoute(t *testing.T) {
	text := "classification is still in flight"
	reviewing := runtime.ApplicationOperatorInput{
		ID: "operator_input_reviewing", RunID: "run-1", Text: text,
		Digest: receiverDigest(text), Status: "reviewing", ReviewID: "review-job-1", Version: 2,
	}
	broker := &tuiApplicationInputBroker{state: runtime.ApplicationInputState{ReviewingInputs: []runtime.ApplicationOperatorInput{reviewing}}}
	m := applicationInputTestModel(t, broker)
	m.applicationFailure = errors.New("test provider barrier")
	m.applicationInputDeliveryRunning = true

	_, command := onApplicationInputState(m, applicationInputStateMsg{after: applicationInputAfterWorkerTurn, state: broker.state})
	if command != nil || m.loop.iter != 0 || m.applicationInputPendingAfter != applicationInputAfterWorkerTurn || len(m.msgs) != 0 {
		t.Fatalf("reviewing boundary command=%v iter=%d pending=%v messages=%#v", command != nil, m.loop.iter, m.applicationInputPendingAfter, m.msgs)
	}

	ready := reviewing
	ready.Status = "ready"
	ready.Version++
	broker.state.ReviewingInputs = nil
	broker.state.ReadyInputs = []runtime.ApplicationOperatorInput{ready}
	stateCommand := m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	stateMessage := stateCommand().(applicationInputStateMsg)
	_, deliveryCommand := onApplicationInputState(m, stateMessage)
	if deliveryCommand == nil {
		t.Fatal("exact reviewed route did not begin native delivery")
	}
	deliveryMessage := deliveryCommand().(applicationInputDeliveryMsg)
	_, nextState := onApplicationInputDelivery(m, deliveryMessage)
	if deliveryMessage.err != nil || nextState == nil {
		t.Fatalf("reviewed delivery=%#v next=%v", deliveryMessage, nextState != nil)
	}
	stateMessage = nextState().(applicationInputStateMsg)
	_, resumed := onApplicationInputState(m, stateMessage)
	if resumed != nil || m.loop.iter != 1 || m.applicationInputPendingAfter != applicationInputAfterNone || len(m.msgs) != 2 || latestUserPrompt(m.msgs[:1]) != text {
		t.Fatalf("reviewed resume command=%v iter=%d pending=%v messages=%#v", resumed != nil, m.loop.iter, m.applicationInputPendingAfter, m.msgs)
	}
}

func TestOpenDeferredApplicationTasksAreSurfacedOnceWithoutNativeTaskWrites(t *testing.T) {
	broker := &tuiApplicationInputBroker{state: runtime.ApplicationInputState{OpenDeferredTaskCount: 1, OpenDeferredTasks: []runtime.ApplicationDeferredTask{
		{ID: "task-1", InputID: "input-1", RunID: "run-1", Title: "follow up separately", Status: "open"},
	}}}
	m := applicationInputTestModel(t, broker)
	command := m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	message := command().(applicationInputStateMsg)
	onApplicationInputState(m, message)
	firstCount := len(m.blocks)
	if firstCount == 0 || !strings.Contains(m.blocks[firstCount-1].body, "1 open deferred") {
		t.Fatalf("deferred task notice blocks=%#v", m.blocks)
	}
	command = m.reconcileApplicationOperatorInput(applicationInputAfterNone)
	message = command().(applicationInputStateMsg)
	onApplicationInputState(m, message)
	if len(m.blocks) != firstCount {
		t.Fatalf("deferred task notice repeated: %d -> %d", firstCount, len(m.blocks))
	}
}

func TestApplicationContinuationAppendsExactOriginalsInSelectedOrder(t *testing.T) {
	first, second := "first original", "second original"
	continuation := runtime.ApplicationContinuation{
		CompletionID: "completion_1", DeliveryID: "continuation_delivery_1", RunID: "run-1",
		InputIDs: []string{"input_2", "input_1"}, Status: "pending",
		Inputs: []runtime.ApplicationContinuationInput{
			{ID: "input_2", Text: second, Digest: receiverDigest(second)},
			{ID: "input_1", Text: first, Digest: receiverDigest(first)},
		},
	}
	broker := &tuiApplicationInputBroker{state: runtime.ApplicationInputState{PendingContinuation: &continuation}}
	m := applicationInputTestModel(t, broker)
	m.loop = nil
	stateCommand := m.reconcileApplicationOperatorInput(applicationInputAfterCompletion)
	stateMessage := stateCommand().(applicationInputStateMsg)
	_, deliveryCommand := onApplicationInputState(m, stateMessage)
	deliveryMessage := deliveryCommand().(applicationInputDeliveryMsg)
	if deliveryMessage.err != nil {
		t.Fatal(deliveryMessage.err)
	}
	onApplicationInputDelivery(m, deliveryMessage)
	loaded, err := runtime.LoadConversation(m.session.WorktreePath)
	if err != nil || len(loaded) != 2 || latestUserPrompt(loaded[:1]) != second || latestUserPrompt(loaded) != first || broker.continuations != 1 {
		t.Fatalf("ordered continuation loaded=%#v commits=%d err=%v", loaded, broker.continuations, err)
	}
}

func TestAutomaticRecoveryPreservesDraftWhenChildCannotOpen(t *testing.T) {
	m := applicationInputTestModel(t, &tuiApplicationInputBroker{})
	m.recoveryPrompt = "blocked exact prompt"
	m.recoveryPluginActive = true
	m.input.SetValue("new draft")
	if command := m.adoptForkedSession("unadmitted-child", string(stadogit.TurnTagRef(m.session.ID, m.session.Turn())), "summary"); command != nil {
		t.Fatal("unavailable automatic child returned a command")
	}
	if m.recoveryPluginActive || !strings.Contains(m.input.Value(), "blocked exact prompt") || !strings.Contains(m.input.Value(), "new draft") || !strings.Contains(m.blocks[len(m.blocks)-1].body, "no live parent session") {
		t.Fatalf("recovery active=%v input=%q blocks=%#v", m.recoveryPluginActive, m.input.Value(), m.blocks)
	}
}

func TestSessionSwitchWaitsForExactApplicationInputDelivery(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationInputDeliveryRunning = true
	if err := m.ensureSessionSwitchAllowed(); err == nil || !strings.Contains(err.Error(), "input persistence") {
		t.Fatalf("session switch delivery gate error = %v", err)
	}
}

func TestTurnBoundaryContinuationIsNotLostBehindPeriodicInputProjection(t *testing.T) {
	m := applicationInputTestModel(t, &tuiApplicationInputBroker{})
	m.state = stateIdle
	m.provider = uatStub{}
	m.applicationInputDeliveryRunning = true // periodic projection already in flight
	if command := m.reconcileApplicationOperatorInput(applicationInputAfterWorkerTurn); command != nil {
		t.Fatal("second projection started instead of joining the in-flight one")
	}
	_, command := onApplicationInputState(m, applicationInputStateMsg{after: applicationInputAfterNone})
	if command == nil || m.loop.iter != 1 || m.applicationInputPendingAfter != applicationInputAfterNone {
		t.Fatalf("boundary continuation command=%v iter=%d pending=%v", command, m.loop.iter, m.applicationInputPendingAfter)
	}
}
