package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/broker/application"
)

const rpcVerificationDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const rpcVerificationTreeDigest = "0123456789abcdef0123456789abcdef01234567"

func TestApplicationVerificationRPCSeparatesGuestRequestFromControllerExecution(t *testing.T) {
	fixture := newLifecycleRPCFixture(t,
		[]string{"session.turn_committed", application.VerificationFinishedEvent},
		"lifecycle:observe:session.turn_committed", "lifecycle:observe:"+application.VerificationFinishedEvent,
		"session:worker:request", "session:verification:request", "session:projection:read")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)

	var worker application.WorkerRun
	raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "worker-request", Operation: "worker.request",
		Payload: json.RawMessage(`{"run_id":"run-1","objective":"finish","prompt":"continue","conflict":"reject"}`),
	})
	if err := json.Unmarshal(raw, &worker); err != nil {
		t.Fatal(err)
	}
	raw = dispatchRPC(t, fixture.service, MethodApplicationWorkerActivate, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: worker.RunID, ExpectedVersion: worker.Version,
	})
	if err := json.Unmarshal(raw, &worker); err != nil {
		t.Fatal(err)
	}

	turnData := json.RawMessage(`{"schema":"stado.dev/session-turn-facts/v1","anchor":{"session_sequence":4294967297,"turn_ref":"git:refs/sessions/session/tree@` + rpcVerificationTreeDigest + `#turn-1-iteration-1","tree_digest":"` + rpcVerificationTreeDigest + `"}}`)
	raw = dispatchRPC(t, fixture.service, MethodApplicationEventPublish, ApplicationEventPublishParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: 1, RequestID: "publish-turn", ID: "turn-1",
		Kind: "session.turn_committed", Data: turnData,
	})
	var turn ApplicationEventResult
	if err := json.Unmarshal(raw, &turn); err != nil {
		t.Fatal(err)
	}

	raw = dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "verification-request", Operation: "verification.request",
		Payload: json.RawMessage(`{"run_id":"run-1","expected_worker_version":2,"source_event_sequence":` + jsonNumber(turn.WALSequence) + `}`),
	})
	var requested application.Verification
	if err := json.Unmarshal(raw, &requested); err != nil || requested.Status != application.VerificationRequested {
		t.Fatalf("requested=%s err=%v", raw, err)
	}

	claim := ApplicationVerificationClaimParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, ID: requested.ID, ExpectedVersion: requested.Version,
		SuiteDigest: rpcVerificationDigest, CommandDigests: []string{rpcVerificationDigest},
	}
	claimRaw, _ := json.Marshal(claim)
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationVerificationClaim, claimRaw); err == nil || !strings.Contains(err.Error(), "not due") {
		t.Fatalf("controller claimed before source ACK: %v", err)
	}
	dispatchRPC(t, fixture.service, MethodApplicationEventsAck, ApplicationEventsAckParams{
		BindingToken: binding.BindingToken, RequestID: "ack-turn", Sequence: turn.WALSequence,
	})
	raw = dispatchRPC(t, fixture.service, MethodApplicationVerificationClaim, claim)
	var running application.Verification
	if err := json.Unmarshal(raw, &running); err != nil || running.Status != application.VerificationRunning {
		t.Fatalf("running=%s err=%v", raw, err)
	}

	raw = dispatchRPC(t, fixture.service, MethodApplicationVerificationFinish, ApplicationVerificationFinishParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, ID: running.ID, ExpectedVersion: running.Version,
		Outcome:  application.VerificationCommandsSucceeded,
		Commands: []application.VerificationCommandFact{{Ordinal: 1, CommandDigest: rpcVerificationDigest, ResultDigest: rpcVerificationDigest, Outcome: "succeeded"}},
	})
	var terminal application.Verification
	if err := json.Unmarshal(raw, &terminal); err != nil || terminal.Status != application.VerificationTerminal {
		t.Fatalf("terminal=%s err=%v", raw, err)
	}

	raw = dispatchRPC(t, fixture.service, MethodApplicationVerificationGet, ApplicationVerificationGetParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, ID: terminal.ID,
	})
	var got ApplicationVerificationGetResult
	if err := json.Unmarshal(raw, &got); err != nil || !got.Found || got.Verification.ID != terminal.ID {
		t.Fatalf("get=%s err=%v", raw, err)
	}
	raw = dispatchRPC(t, fixture.service, MethodApplicationVerificationGet, ApplicationVerificationGetParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken,
	})
	if err := json.Unmarshal(raw, &got); err != nil || got.Found {
		t.Fatalf("next after terminal=%s err=%v", raw, err)
	}

	guestClaim, _ := json.Marshal(ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "guest-claim", Operation: "verification.claim", Payload: json.RawMessage(`{}`),
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, guestClaim); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("guest reached verification claim: %v", err)
	}
	unauthenticated := claim
	unauthenticated.ControllerToken = "controller_wrong"
	unauthenticatedRaw, _ := json.Marshal(unauthenticated)
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationVerificationClaim, unauthenticatedRaw); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("unauthenticated claim error=%v", err)
	}
}

func TestApplicationVerificationRequestRequiresExactCapability(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"session.turn_committed"},
		"lifecycle:observe:session.turn_committed", "session:worker:request")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	params, _ := json.Marshal(ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "request", Operation: "verification.request",
		Payload: json.RawMessage(`{"run_id":"run","expected_worker_version":1,"source_event_sequence":1}`),
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, params); err == nil || !strings.Contains(err.Error(), "session:verification:request") {
		t.Fatalf("missing capability error=%v", err)
	}
}

func jsonNumber(value uint64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
