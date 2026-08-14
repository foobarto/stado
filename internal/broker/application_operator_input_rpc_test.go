package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/broker/application"
)

func TestApplicationOperatorInputRPCIsTargetedCapabilityGatedAndRouteBeforeAck(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{application.OperatorInputQueuedEvent},
		"lifecycle:observe:"+application.OperatorInputQueuedEvent,
		"session:worker:request", "session:input:route", "session:projection:read")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	call := func(requestID, operation, payload string, out any) {
		t.Helper()
		raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
			BindingToken: binding.BindingToken, RequestID: requestID,
			Operation: operation, Payload: json.RawMessage(payload),
		})
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var requested application.WorkerRun
	call("worker:request", "worker.request", `{"run_id":"run-input","objective":"focused work","prompt":"continue focused work","conflict":"reject"}`, &requested)
	var active application.WorkerRun
	activeRaw := dispatchRPC(t, fixture.service, MethodApplicationWorkerActivate, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: requested.RunID, ExpectedVersion: requested.Version,
	})
	if err := json.Unmarshal(activeRaw, &active); err != nil {
		t.Fatal(err)
	}

	capturedRaw := dispatchRPC(t, fixture.service, MethodApplicationInputCapture, ApplicationInputCaptureParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: 1, RequestID: "native:capture:1", Text: "preserve this unrelated request",
	})
	var captured application.OperatorInput
	if err := json.Unmarshal(capturedRaw, &captured); err != nil {
		t.Fatal(err)
	}
	events, rawEvents := nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
	if len(events) != 1 || events[0].Kind != application.OperatorInputQueuedEvent || events[0].WALSequence != captured.WALSequence || strings.Contains(string(rawEvents), "target_plugin") || strings.Contains(string(rawEvents), "subject_id") {
		t.Fatalf("projected input events=%s", rawEvents)
	}

	ackRaw, _ := json.Marshal(ApplicationEventsAckParams{
		BindingToken: binding.BindingToken, RequestID: "ack:before-route", Sequence: captured.WALSequence,
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsAck, ackRaw); err == nil || !strings.Contains(err.Error(), "requires application routing") {
		t.Fatalf("ack before route error=%v", err)
	}
	var routed application.OperatorInput
	call("input:route", "input.route", `{"input_id":"`+captured.ID+`","run_id":"run-input","expected_version":1,"disposition":"defer","label":"later task","rationale":"outside the current objective"}`, &routed)
	if routed.Status != application.OperatorInputDeferred || routed.TaskID == "" || routed.Text != captured.Text {
		t.Fatalf("routed input=%+v", routed)
	}
	dispatchRPC(t, fixture.service, MethodApplicationEventsAck, ApplicationEventsAckParams{
		BindingToken: binding.BindingToken, RequestID: "ack:after-route", Sequence: captured.WALSequence,
	})
	var projection application.Projection
	call("projection:tasks", "projection.read", `{"deferred_task_limit":1}`, &projection)
	if len(projection.DeferredTasks) != 1 || projection.DeferredTasks[0].InputID != captured.ID || projection.DeferredTasks[0].Text != captured.Text {
		t.Fatalf("deferred projection=%+v", projection.DeferredTasks)
	}
	var nativeState ApplicationInputStateResult
	rawState := dispatchRPC(t, fixture.service, MethodApplicationInputState, ApplicationInputStateParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken, ExpectedGeneration: 1,
	})
	if err := json.Unmarshal(rawState, &nativeState); err != nil {
		t.Fatal(err)
	}
	if nativeState.OpenDeferredTaskCount != 1 || nativeState.OpenDeferredTruncated || len(nativeState.OpenDeferredTasks) != 1 || nativeState.OpenDeferredTasks[0].InputID != captured.ID || nativeState.OpenDeferredTasks[0].Status != application.DeferredTaskOpen {
		t.Fatalf("native open deferred projection=%+v", nativeState.OpenDeferredTasks)
	}

	secondRaw := dispatchRPC(t, fixture.service, MethodApplicationInputCapture, ApplicationInputCaptureParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: 1, RequestID: "native:capture:2", Text: "review this asynchronously",
	})
	var second application.OperatorInput
	if err := json.Unmarshal(secondRaw, &second); err != nil {
		t.Fatal(err)
	}
	var claimed application.OperatorInput
	call("input:claim", "input.claim", `{"input_id":"`+second.ID+`","run_id":"run-input","expected_version":1,"review_id":"job-2"}`, &claimed)
	if claimed.Status != application.OperatorInputReviewing || claimed.ReviewID != "job-2" {
		t.Fatalf("claimed input=%+v", claimed)
	}
	dispatchRPC(t, fixture.service, MethodApplicationEventsAck, ApplicationEventsAckParams{
		BindingToken: binding.BindingToken, RequestID: "ack:after-claim", Sequence: second.WALSequence,
	})
	call("projection:reviewing", "projection.read", `{}`, &projection)
	if len(projection.ReviewingInputs) != 1 || projection.ReviewingInputs[0].ID != second.ID || projection.ReviewingInputs[0].ReviewID != "job-2" {
		t.Fatalf("reviewing projection=%+v", projection.ReviewingInputs)
	}
	rawState = dispatchRPC(t, fixture.service, MethodApplicationInputState, ApplicationInputStateParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken, ExpectedGeneration: 1,
	})
	if err := json.Unmarshal(rawState, &nativeState); err != nil || len(nativeState.ReviewingInputs) != 1 || nativeState.ReviewingInputs[0].ID != second.ID {
		t.Fatalf("native reviewing projection=%+v err=%v", nativeState.ReviewingInputs, err)
	}
	var reviewed application.OperatorInput
	call("input:reviewed-route", "input.route", `{"input_id":"`+second.ID+`","run_id":"run-input","expected_version":2,"review_id":"job-2","disposition":"deliver"}`, &reviewed)
	if reviewed.Status != application.OperatorInputReady || reviewed.ReviewID != "job-2" {
		t.Fatalf("reviewed route=%+v", reviewed)
	}

	withoutCap := newLifecycleRPCFixture(t, nil, "session:worker:request")
	withoutCapBinding := bindLifecycleRPC(t, withoutCap, withoutCap.manifest)
	payload, _ := json.Marshal(ApplicationCallParams{
		BindingToken: withoutCapBinding.BindingToken, RequestID: "route:denied", Operation: "input.route",
		Payload: json.RawMessage(`{"input_id":"input-1","run_id":"run-1","expected_version":1,"disposition":"defer"}`),
	})
	if _, err := withoutCap.service.Dispatch(context.Background(), MethodApplicationCall, payload); err == nil || !strings.Contains(err.Error(), "session:input:route") {
		t.Fatalf("missing route capability error=%v", err)
	}
	payload, _ = json.Marshal(ApplicationCallParams{
		BindingToken: withoutCapBinding.BindingToken, RequestID: "claim:denied", Operation: "input.claim",
		Payload: json.RawMessage(`{"input_id":"input-1","run_id":"run-1","expected_version":1,"review_id":"job"}`),
	})
	if _, err := withoutCap.service.Dispatch(context.Background(), MethodApplicationCall, payload); err == nil || !strings.Contains(err.Error(), "session:input:route") {
		t.Fatalf("missing claim capability error=%v", err)
	}
}

func TestApplicationOperatorInputNativeControllerProjectionCommitAndScope(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{application.OperatorInputQueuedEvent},
		"lifecycle:observe:"+application.OperatorInputQueuedEvent,
		"session:worker:request", "session:input:route")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	var requested application.WorkerRun
	raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "request", Operation: "worker.request",
		Payload: json.RawMessage(`{"run_id":"run-native","objective":"focused","prompt":"continue","conflict":"reject"}`),
	})
	if err := json.Unmarshal(raw, &requested); err != nil {
		t.Fatal(err)
	}
	dispatchRPC(t, fixture.service, MethodApplicationWorkerActivate, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: requested.RunID, ExpectedVersion: requested.Version,
	})
	var captured application.OperatorInput
	raw = dispatchRPC(t, fixture.service, MethodApplicationInputCapture, ApplicationInputCaptureParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: 1, RequestID: "capture", Text: "exact related input",
	})
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "route", Operation: "input.route",
		Payload: json.RawMessage(`{"input_id":"` + captured.ID + `","run_id":"run-native","expected_version":1,"disposition":"deliver"}`),
	})
	var state ApplicationInputStateResult
	raw = dispatchRPC(t, fixture.service, MethodApplicationInputState, ApplicationInputStateParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken, ExpectedGeneration: 1,
	})
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.ActiveWorkerRun == nil || state.ActiveWorkerRun.RunID != "run-native" || len(state.ReadyInputs) != 1 || state.ReadyInputs[0].ID != captured.ID {
		t.Fatalf("native input projection = %+v", state)
	}

	wrong, _ := json.Marshal(ApplicationInputCommitParams{
		SessionID: fixture.session.SessionID, ControllerToken: "foreign-controller", ExpectedGeneration: 1,
		RequestID: "commit:foreign", InputID: captured.ID, ExpectedVersion: state.ReadyInputs[0].Version,
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationInputCommit, wrong); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("foreign controller commit error = %v", err)
	}
	stale, _ := json.Marshal(ApplicationInputStateParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken, ExpectedGeneration: 2,
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationInputState, stale); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale generation state error = %v", err)
	}

	var delivered application.OperatorInput
	raw = dispatchRPC(t, fixture.service, MethodApplicationInputCommit, ApplicationInputCommitParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken, ExpectedGeneration: 1,
		RequestID: "commit", InputID: captured.ID, ExpectedVersion: state.ReadyInputs[0].Version,
	})
	if err := json.Unmarshal(raw, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Status != application.OperatorInputDelivered || delivered.ReceiverInputID != captured.ID {
		t.Fatalf("native delivery = %+v", delivered)
	}
}
