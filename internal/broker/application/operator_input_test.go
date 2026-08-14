package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

func activateInputTestRun(t *testing.T, f *fixture, auth Authority, runID string) WorkerRun {
	t.Helper()
	requested, err := f.service.RequestWorkerRun(context.Background(), auth, WorkerRunRequest{
		RunID: runID, Objective: "bounded objective", Prompt: "continue the bounded worker run",
		Conflict: WorkerRunReplaceOperatorLoop,
	}, "request:"+runID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: runID, ExpectedVersion: requested.Version}, "activate:"+runID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func TestOperatorInputCaptureTargetsExactActiveRunAndRequiresRouteBeforeAck(t *testing.T) {
	f := newFixture(t)
	scope := SessionScope{SessionID: "session-1", Generation: 3}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, "do not lose this", "capture:before"); !errors.Is(err, ErrNoInputOwner) {
		t.Fatalf("capture without owner error = %v", err)
	}
	auth := testAuthority("plugin#input-owner")
	activateInputTestRun(t, f, auth, "run-1")

	input, err := f.service.CaptureOperatorInput(context.Background(), scope, "do not lose this", "capture:1")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.CaptureOperatorInput(context.Background(), scope, "do not lose this", "capture:1")
	if err != nil || !reflect.DeepEqual(input, retry) {
		t.Fatalf("capture retry = %+v, %v", retry, err)
	}
	if input.PluginID != auth.PluginID || input.RunID != "run-1" || input.Ordinal != 1 || input.Version != 1 || input.Status != OperatorInputQueued || input.Digest == "" {
		t.Fatalf("captured input = %+v", input)
	}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, "changed", "capture:1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed capture retry error = %v", err)
	}

	pending, _, err := f.service.PendingEvents(context.Background(), auth, []string{OperatorInputQueuedEvent}, 10)
	if err != nil || len(pending) != 1 || pending[0].TargetPlugin != auth.PluginID || pending[0].SubjectID != input.ID || !strings.Contains(string(pending[0].Data), "do not lose this") {
		t.Fatalf("pending input events = %+v, %v", pending, err)
	}
	other := testAuthority("plugin#other")
	otherPending, _, err := f.service.PendingEvents(context.Background(), other, []string{OperatorInputQueuedEvent}, 10)
	if err != nil || len(otherPending) != 0 {
		t.Fatalf("targeted input leaked = %+v, %v", otherPending, err)
	}
	if _, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: input.WALSequence}, "ack:before-route"); !errors.Is(err, ErrOperatorInputQueued) {
		t.Fatalf("ack before route error = %v", err)
	}

	routed, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: input.ID, RunID: input.RunID, ExpectedVersion: input.Version,
		Disposition: OperatorInputDeliver, Label: "related", Rationale: "same objective",
	}, "route:1")
	if err != nil {
		t.Fatal(err)
	}
	if routed.Status != OperatorInputReady || routed.Text != input.Text || routed.Digest != input.Digest || routed.Version != 2 {
		t.Fatalf("routed input = %+v", routed)
	}
	// Routing and cursor acknowledgement are separate durable mutations. If the
	// host crashes between them, replay must not invoke the mandatory route
	// callback again; the native dispatcher may still acknowledge the settled
	// event after verifying its signed subscription.
	pending, _, err = f.service.PendingEvents(context.Background(), auth, []string{OperatorInputQueuedEvent}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("settled input event replayed = %+v, %v", pending, err)
	}
	acked, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: input.WALSequence}, "ack:after-route")
	if err != nil || acked.Sequence != input.WALSequence {
		t.Fatalf("ack after route = %+v, %v", acked, err)
	}

	enforcement, err := f.service.ProjectEnforcement(context.Background(), scope)
	if err != nil || len(enforcement.ReadyOperatorInputs) != 1 || enforcement.ReadyOperatorInputs[0].ID != input.ID {
		t.Fatalf("ready enforcement = %+v, %v", enforcement, err)
	}
	delivered, err := f.service.CommitOperatorInput(context.Background(), scope, OperatorInputCommit{
		InputID: input.ID, ExpectedVersion: routed.Version, ReceiverInputID: input.ID,
	}, "deliver:1")
	if err != nil || delivered.Status != OperatorInputDelivered || delivered.ReceiverInputID != input.ID || delivered.Recovered {
		t.Fatalf("delivered input = %+v, %v", delivered, err)
	}
	reloaded := New(f.store)
	reloaded.now = f.service.now
	enforcement, err = reloaded.ProjectEnforcement(context.Background(), scope)
	if err != nil || len(enforcement.ReadyOperatorInputs) != 0 || len(enforcement.RecoveryOperatorInputs) != 0 {
		t.Fatalf("reloaded enforcement = %+v, %v", enforcement, err)
	}
}

func TestOperatorInputAsyncClaimAllowsAckAndExactRouteAcrossRestart(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#async-input")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	activateInputTestRun(t, f, auth, "run")
	input, err := f.service.CaptureOperatorInput(context.Background(), scope, "classify asynchronously", "capture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: input.WALSequence}, "ack-before-claim"); !errors.Is(err, ErrOperatorInputQueued) {
		t.Fatalf("ack before claim error = %v", err)
	}
	claimInput := OperatorInputClaim{InputID: input.ID, RunID: input.RunID, ExpectedVersion: input.Version, ReviewID: "review-job-1"}
	claimed, err := f.service.ClaimOperatorInput(context.Background(), auth, claimInput, "claim")
	if err != nil || claimed.Status != OperatorInputReviewing || claimed.Version != input.Version+1 || claimed.ReviewID != claimInput.ReviewID || claimed.Text != input.Text || claimed.Digest != input.Digest || claimed.Ordinal != input.Ordinal {
		t.Fatalf("claimed input = %+v, %v", claimed, err)
	}
	recordsAfterClaim := len(f.store.Records())
	retry, err := f.service.ClaimOperatorInput(context.Background(), auth, claimInput, "claim")
	if err != nil || !reflect.DeepEqual(retry, claimed) || len(f.store.Records()) != recordsAfterClaim {
		t.Fatalf("claim replay = %+v, %v records=%d", retry, err, len(f.store.Records()))
	}
	changedClaim := claimInput
	changedClaim.ReviewID = "different-review-job"
	if _, err := f.service.ClaimOperatorInput(context.Background(), auth, changedClaim, "claim"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed claim replay error = %v", err)
	}
	pending, _, err := f.service.PendingEvents(context.Background(), auth, []string{OperatorInputQueuedEvent}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("claimed mandatory event replayed = %+v, %v", pending, err)
	}
	acked, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: input.WALSequence}, "ack-after-claim")
	if err != nil || acked.Sequence != input.WALSequence {
		t.Fatalf("ack after claim = %+v, %v", acked, err)
	}
	down, err := f.service.PublishEvent(context.Background(), scope, EventInput{ID: "job-down", Kind: "agent.down", Data: []byte(`{"job_id":"review-job-1"}`)}, "publish-down")
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err = f.service.PendingEvents(context.Background(), auth, []string{OperatorInputQueuedEvent, "agent.down"}, 10)
	if err != nil || len(pending) != 1 || pending[0].WALSequence != down.WALSequence || pending[0].Kind != "agent.down" {
		t.Fatalf("post-claim pending events = %+v, %v", pending, err)
	}

	reloaded := New(f.store)
	reloaded.now = f.service.now
	projection, err := reloaded.Project(context.Background(), auth, ProjectionOptions{})
	if err != nil || len(projection.ReviewingInputs) != 1 || !reflect.DeepEqual(projection.ReviewingInputs[0], claimed) {
		t.Fatalf("reloaded reviewing projection = %+v, %v", projection.ReviewingInputs, err)
	}
	reloadedRetry, err := reloaded.ClaimOperatorInput(context.Background(), auth, claimInput, "claim")
	if err != nil || !reflect.DeepEqual(reloadedRetry, claimed) || len(f.store.Records()) != recordsAfterClaim+2 { // ack + agent.down followed claim
		t.Fatalf("reloaded claim replay = %+v, %v records=%d", reloadedRetry, err, len(f.store.Records()))
	}
	if _, err := reloaded.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: claimed.ID, RunID: claimed.RunID, ExpectedVersion: claimed.Version,
		ReviewID: "different-job", Disposition: OperatorInputDeliver,
	}, "route-wrong-review"); !errors.Is(err, ErrScope) {
		t.Fatalf("mismatched review route error = %v", err)
	}
	other := testAuthority("plugin#other-input")
	if _, err := reloaded.ClaimOperatorInput(context.Background(), other, OperatorInputClaim{
		InputID: input.ID, RunID: input.RunID, ExpectedVersion: input.Version, ReviewID: claimInput.ReviewID,
	}, "claim-other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope claim error = %v", err)
	}
	routed, err := reloaded.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: claimed.ID, RunID: claimed.RunID, ExpectedVersion: claimed.Version,
		ReviewID: claimed.ReviewID, Disposition: OperatorInputDeliver, Label: "related",
	}, "route")
	if err != nil || routed.Status != OperatorInputReady || routed.ReviewID != claimed.ReviewID || routed.Text != input.Text || routed.Digest != input.Digest {
		t.Fatalf("reviewed route = %+v, %v", routed, err)
	}
	ackedDown, err := reloaded.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: down.WALSequence}, "ack-agent-down")
	if err != nil || ackedDown.Sequence != down.WALSequence {
		t.Fatalf("ack reviewed child result = %+v, %v", ackedDown, err)
	}
}

func TestReviewingOperatorInputFoldRejectsForgedCorrelationAndShape(t *testing.T) {
	auth := testAuthority("plugin#review-fold")
	input := OperatorInput{
		ID: "operator_input_1", SessionID: auth.SessionID, Generation: auth.Generation,
		PluginID: auth.PluginID, RunID: "run", Ordinal: 1, Version: 1,
		Text: "immutable input", Digest: operatorInputDigest("immutable input"),
		Status: OperatorInputQueued, CreatedAt: testNow, UpdatedAt: testNow,
	}
	reviewing := input
	reviewing.Version = 2
	reviewing.Status = OperatorInputReviewing
	reviewing.ReviewID = "review-job-1"
	reviewing.UpdatedAt = testNow.Add(time.Second)
	ready := reviewing
	ready.Version = 3
	ready.Status = OperatorInputReady
	ready.UpdatedAt = testNow.Add(2 * time.Second)

	makeRecord := func(sequence uint64, eventType string, meta eventMeta, value OperatorInput) wal.Record {
		data, err := json.Marshal(eventEnvelope{Meta: meta, OperatorInput: &value})
		if err != nil {
			t.Fatal(err)
		}
		return wal.Record{Sequence: sequence, Transaction: wal.Transaction{Events: []wal.Event{{
			Store: storeName, Type: eventType, Session: auth.SessionID, Data: data,
		}}}}
	}
	brokerMeta := eventMeta{Schema: eventSchema, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: "stado.dev/broker", RequestDigest: "capture"}
	applicationMeta := eventMeta{Schema: eventSchema, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, RequestDigest: "review"}
	queuedRecord := makeRecord(1, "operator_input.queued", brokerMeta, input)
	reviewingRecord := makeRecord(2, "operator_input.reviewing", applicationMeta, reviewing)
	if _, err := fold([]wal.Record{queuedRecord, reviewingRecord, makeRecord(3, "operator_input.ready", applicationMeta, ready)}); err != nil {
		t.Fatalf("valid reviewing fold: %v", err)
	}

	for name, forged := range map[string]OperatorInput{
		"claim with policy fields":   func() OperatorInput { value := reviewing; value.Label = "trusted"; return value }(),
		"changed review correlation": func() OperatorInput { value := ready; value.ReviewID = "other-job"; return value }(),
		"changed immutable text": func() OperatorInput {
			value := ready
			value.Text = "rewritten"
			value.Digest = operatorInputDigest(value.Text)
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			records := []wal.Record{queuedRecord}
			eventType := "operator_input.reviewing"
			if forged.Version == 3 {
				records = append(records, reviewingRecord)
				eventType = "operator_input.ready"
			}
			records = append(records, makeRecord(uint64(len(records)+1), eventType, applicationMeta, forged))
			if _, err := fold(records); err == nil {
				t.Fatal("fold accepted forged reviewing transition")
			}
		})
	}
}

func TestReviewingInputBlocksCompletionBackpressureAndRecoversOnTerminalRun(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPendingOperatorInputs = 1
	f := newFixtureWithLimits(t, limits)
	auth := testAuthority("plugin#review-recovery")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	active := activateInputTestRun(t, f, auth, "run")
	input, err := f.service.CaptureOperatorInput(context.Background(), scope, "review before completion", "capture")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.service.ClaimOperatorInput(context.Background(), auth, OperatorInputClaim{
		InputID: input.ID, RunID: input.RunID, ExpectedVersion: input.Version, ReviewID: "review-timeout-1",
	}, "claim")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, "backpressure remains", "capture-second"); !errors.Is(err, ErrLimit) {
		t.Fatalf("reviewing backpressure error = %v", err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: active.RunID, Summary: "stale conclusion"}, "complete"); !errors.Is(err, ErrOperatorInputUnresolved) {
		t.Fatalf("reviewing completion error = %v", err)
	}
	enforcement, err := f.service.ProjectEnforcement(context.Background(), scope)
	if err != nil || len(enforcement.ReviewingOperatorInputs) != 1 || enforcement.ReviewingOperatorInputs[0].ID != claimed.ID || len(enforcement.ReadyOperatorInputs) != 0 {
		t.Fatalf("reviewing enforcement = %+v, %v", enforcement, err)
	}
	cancelled, err := f.service.CancelWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: active.RunID, ExpectedVersion: active.Version, Reason: "review job orphaned"}, "cancel")
	if err != nil || cancelled.Status != WorkerRunCancelled {
		t.Fatalf("cancelled run = %+v, %v", cancelled, err)
	}
	enforcement, err = f.service.ProjectEnforcement(context.Background(), scope)
	if err != nil || len(enforcement.ReviewingOperatorInputs) != 0 || len(enforcement.RecoveryOperatorInputs) != 1 || enforcement.RecoveryOperatorInputs[0].ID != claimed.ID {
		t.Fatalf("terminal reviewing recovery = %+v, %v", enforcement, err)
	}
	delivered, err := f.service.CommitOperatorInput(context.Background(), scope, OperatorInputCommit{
		InputID: claimed.ID, ExpectedVersion: claimed.Version, ReceiverInputID: claimed.ID, Recovery: true,
	}, "recover")
	if err != nil || delivered.Status != OperatorInputDelivered || !delivered.Recovered || delivered.Text != claimed.Text || delivered.ReviewID != claimed.ReviewID {
		t.Fatalf("recovered reviewing input = %+v, %v", delivered, err)
	}
}

func TestOperatorInputCaptureAndCompletionSerializeWithoutDroppingInput(t *testing.T) {
	for i := 0; i < 12; i++ {
		f := newFixture(t)
		auth := testAuthority(fmt.Sprintf("plugin#capture-complete-%d", i))
		scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
		activateInputTestRun(t, f, auth, "run")
		var captured OperatorInput
		var captureErr, completionErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			captured, captureErr = f.service.CaptureOperatorInput(context.Background(), scope, "concurrent immutable input", "capture")
		}()
		go func() {
			defer wg.Done()
			_, completionErr = f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run", Summary: "candidate conclusion"}, "complete")
		}()
		wg.Wait()
		switch {
		case captureErr == nil:
			if captured.Status != OperatorInputQueued || !errors.Is(completionErr, ErrOperatorInputUnresolved) {
				t.Fatalf("capture-first result = %+v, completion=%v", captured, completionErr)
			}
		case completionErr == nil:
			if !errors.Is(captureErr, ErrNoInputOwner) {
				t.Fatalf("completion-first capture error = %v", captureErr)
			}
		default:
			t.Fatalf("unexpected race errors capture=%v completion=%v", captureErr, completionErr)
		}
	}
}

func TestDeferredInputsAreCanonicalTasksAndCompletionOrdersExactSet(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#deferred")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	activateInputTestRun(t, f, auth, "run-deferred")

	var deferred []OperatorInput
	for i, text := range []string{"first unrelated request", "second unrelated request"} {
		captured, err := f.service.CaptureOperatorInput(context.Background(), scope, text, "capture:defer:"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		routed, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
			InputID: captured.ID, RunID: captured.RunID, ExpectedVersion: captured.Version,
			Disposition: OperatorInputDefer, Label: "task " + string(rune('1'+i)), Rationale: "separate work",
		}, "route:defer:"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		deferred = append(deferred, routed)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{DeferredTaskLimit: 1})
	if err != nil || len(projection.DeferredTasks) != 1 || !projection.DeferredTasksTruncated || projection.DeferredTasks[0].InputID != deferred[0].ID || projection.DeferredTasks[0].Text != "first unrelated request" {
		t.Fatalf("first deferred page = %+v, %v", projection, err)
	}
	secondPage, err := f.service.Project(context.Background(), auth, ProjectionOptions{DeferredTaskLimit: 1, DeferredTaskAfterOrdinal: projection.DeferredTaskNextOrdinal})
	if err != nil || len(secondPage.DeferredTasks) != 1 || secondPage.DeferredTasksTruncated || secondPage.DeferredTasks[0].InputID != deferred[1].ID {
		t.Fatalf("second deferred page = %+v, %v", secondPage, err)
	}

	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run-deferred", ContinuationInputIDs: []string{deferred[0].ID}}, "complete:omitted"); !errors.Is(err, ErrOperatorInputUnresolved) {
		t.Fatalf("completion omitted task error = %v", err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run-deferred", ContinuationInputIDs: []string{deferred[0].ID, deferred[0].ID}}, "complete:duplicate"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("completion duplicate task error = %v", err)
	}
	ordered := []string{deferred[1].ID, deferred[0].ID}
	completion, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{
		RunID: "run-deferred", Summary: "quality workflow complete", ContinuationInputIDs: ordered,
	}, "complete:exact")
	if err != nil || !reflect.DeepEqual(completion.ContinuationInputIDs, ordered) {
		t.Fatalf("completion = %+v, %v", completion, err)
	}
	enforcement, err := f.service.ProjectEnforcement(context.Background(), scope)
	if err != nil || enforcement.PendingContinuation == nil || !reflect.DeepEqual(enforcement.PendingContinuation.InputIDs, ordered) || enforcement.PendingContinuation.Status != ContinuationPending {
		t.Fatalf("pending continuation = %+v, %v", enforcement.PendingContinuation, err)
	}
	inputs, err := f.service.PendingContinuationInputs(context.Background(), scope, completion.ID)
	if err != nil || len(inputs) != 2 || inputs[0].ID != deferred[1].ID || inputs[0].Text != "second unrelated request" || inputs[0].Digest != deferred[1].Digest || inputs[1].ID != deferred[0].ID || inputs[1].Text != "first unrelated request" || inputs[1].Digest != deferred[0].Digest {
		t.Fatalf("ordered continuation inputs = %+v, %v", inputs, err)
	}
	pendingTasks, err := f.service.Project(context.Background(), auth, ProjectionOptions{DeferredTaskLimit: 2})
	if err != nil || len(pendingTasks.DeferredTasks) != 2 || pendingTasks.DeferredTasks[0].Status != DeferredTaskPendingContinuation || pendingTasks.DeferredTasks[1].Status != DeferredTaskPendingContinuation {
		t.Fatalf("pending continuation tasks = %+v, %v", pendingTasks.DeferredTasks, err)
	}
	continuation, err := f.service.CommitContinuation(context.Background(), scope, ContinuationCommit{
		CompletionID: completion.ID, RunID: completion.RunID, ReceiverInputID: completion.ContinuationDeliveryID,
	}, "continuation:commit")
	if err != nil || continuation.Status != ContinuationDelivered || continuation.ReceiverInputID != completion.ContinuationDeliveryID {
		t.Fatalf("continuation commit = %+v, %v", continuation, err)
	}
	retry, err := f.service.CommitContinuation(context.Background(), scope, ContinuationCommit{
		CompletionID: completion.ID, RunID: completion.RunID, ReceiverInputID: completion.ContinuationDeliveryID,
	}, "continuation:commit")
	if err != nil || !reflect.DeepEqual(continuation, retry) {
		t.Fatalf("continuation retry = %+v, %v", retry, err)
	}
	reloaded := New(f.store)
	reloaded.now = f.service.now
	enforcement, err = reloaded.ProjectEnforcement(context.Background(), scope)
	if err != nil || enforcement.PendingContinuation != nil || enforcement.LatestCompletion == nil || !enforcement.LatestCompletion.ContinuationDelivered {
		t.Fatalf("reloaded continuation projection = %+v, %v", enforcement, err)
	}
	continuedTasks, err := reloaded.Project(context.Background(), auth, ProjectionOptions{DeferredTaskLimit: 2})
	if err != nil || len(continuedTasks.DeferredTasks) != 2 || continuedTasks.DeferredTasks[0].Status != DeferredTaskContinued || continuedTasks.DeferredTasks[1].Status != DeferredTaskContinued {
		t.Fatalf("continued tasks = %+v, %v", continuedTasks.DeferredTasks, err)
	}
}

func TestCompletionRejectsUnroutedOrReadyInput(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#completion-input")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	activateInputTestRun(t, f, auth, "run-blocked")
	captured, err := f.service.CaptureOperatorInput(context.Background(), scope, "classify me", "capture:blocked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run-blocked"}, "complete:queued"); !errors.Is(err, ErrOperatorInputUnresolved) {
		t.Fatalf("queued completion error = %v", err)
	}
	routed, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: captured.ID, RunID: captured.RunID, ExpectedVersion: captured.Version, Disposition: OperatorInputDeliver,
	}, "route:blocked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run-blocked"}, "complete:ready"); !errors.Is(err, ErrOperatorInputUnresolved) {
		t.Fatalf("ready completion error = %v", err)
	}
	if _, err := f.service.CommitOperatorInput(context.Background(), scope, OperatorInputCommit{InputID: captured.ID, ExpectedVersion: routed.Version, ReceiverInputID: captured.ID}, "deliver:blocked"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run-blocked"}, "complete:delivered"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDeferredNativeSummaryIsBounded(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#summary-bound")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	activateInputTestRun(t, f, auth, "run-summary-bound")
	for i := 0; i < maxNativeDeferredSummaries+1; i++ {
		captured, err := f.service.CaptureOperatorInput(context.Background(), scope, fmt.Sprintf("deferred request %02d", i), fmt.Sprintf("capture:summary:%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
			InputID: captured.ID, RunID: captured.RunID, ExpectedVersion: captured.Version,
			Disposition: OperatorInputDefer, Label: fmt.Sprintf("task %02d", i),
		}, fmt.Sprintf("route:summary:%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := f.service.ProjectEnforcement(context.Background(), scope)
	if err != nil || projection.OpenDeferredTaskCount != maxNativeDeferredSummaries+1 || !projection.OpenDeferredTruncated || len(projection.OpenDeferredTasks) != maxNativeDeferredSummaries || projection.OpenDeferredTasks[0].Title != "task 00" {
		t.Fatalf("bounded native deferred summary = %+v, %v", projection, err)
	}
}

func TestTerminalRunRecoveryPreservesQueuedAndReadyInputInOrder(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#recovery")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	active := activateInputTestRun(t, f, auth, "run-recovery")
	queued, err := f.service.CaptureOperatorInput(context.Background(), scope, "queued original", "capture:q")
	if err != nil {
		t.Fatal(err)
	}
	readyCapture, err := f.service.CaptureOperatorInput(context.Background(), scope, "ready original", "capture:r")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: readyCapture.ID, RunID: readyCapture.RunID, ExpectedVersion: readyCapture.Version, Disposition: OperatorInputDeliver,
	}, "route:r")
	if err != nil {
		t.Fatal(err)
	}
	deferredCapture, err := f.service.CaptureOperatorInput(context.Background(), scope, "separate later task", "capture:d")
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: deferredCapture.ID, RunID: deferredCapture.RunID, ExpectedVersion: deferredCapture.Version,
		Disposition: OperatorInputDefer, Label: "later task",
	}, "route:d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CancelWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: active.RunID, ExpectedVersion: active.Version, Reason: "operator cancelled application"}, "cancel:recovery"); err != nil {
		t.Fatal(err)
	}
	enforcement, err := f.service.ProjectEnforcement(context.Background(), scope)
	if err != nil || len(enforcement.RecoveryOperatorInputs) != 2 || enforcement.RecoveryOperatorInputs[0].ID != queued.ID || enforcement.RecoveryOperatorInputs[1].ID != ready.ID || enforcement.OpenDeferredTaskCount != 1 || enforcement.OpenDeferredTruncated || len(enforcement.OpenDeferredTasks) != 1 || enforcement.OpenDeferredTasks[0].InputID != deferred.ID {
		t.Fatalf("recovery inputs = %+v, %v", enforcement.RecoveryOperatorInputs, err)
	}
	for _, input := range enforcement.RecoveryOperatorInputs {
		committed, err := f.service.CommitOperatorInput(context.Background(), scope, OperatorInputCommit{
			InputID: input.ID, ExpectedVersion: input.Version, ReceiverInputID: input.ID, Recovery: true,
		}, "recover:"+input.ID)
		if err != nil || !committed.Recovered || committed.Status != OperatorInputDelivered {
			t.Fatalf("recovered input = %+v, %v", committed, err)
		}
	}
	otherScope := SessionScope{SessionID: scope.SessionID, Generation: scope.Generation + 1}
	if _, err := f.service.CommitOperatorInput(context.Background(), otherScope, OperatorInputCommit{InputID: queued.ID, ExpectedVersion: queued.Version, ReceiverInputID: "wrong", Recovery: true}, "wrong-generation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-generation recovery error = %v", err)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{DeferredTaskLimit: 1})
	if err != nil || len(projection.DeferredTasks) != 1 || projection.DeferredTasks[0].InputID != deferred.ID || projection.DeferredTasks[0].Status != DeferredTaskOpen {
		t.Fatalf("terminal deferred task = %+v, %v", projection.DeferredTasks, err)
	}
}

func TestOperatorInputBoundsAndBackpressureFailClosed(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPendingOperatorInputs = 2
	limits.MaxOperatorInputs = 3
	f := newFixtureWithLimits(t, limits)
	auth := testAuthority("plugin#bounds")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	activateInputTestRun(t, f, auth, "run-bounds")

	exact := strings.Repeat("x", MaxOperatorInputBytes)
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, exact, "capture:exact"); err != nil {
		t.Fatalf("exact input bound error = %v", err)
	}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, strings.Repeat("x", MaxOperatorInputBytes+1), "capture:oversize"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize input error = %v", err)
	}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, string([]byte{0xff}), "capture:utf8"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 input error = %v", err)
	}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, "second pending", "capture:second"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CaptureOperatorInput(context.Background(), scope, "must stay blocked", "capture:third"); !errors.Is(err, ErrLimit) {
		t.Fatalf("backpressure error = %v", err)
	}
}
