package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

type retainedRPCFixture struct {
	service *Service
	session SessionHandle
	token   string
	subject string
}

func newRetainedRPCFixture(t *testing.T) retainedRPCFixture {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(DefaultPolicy(), nil)
	if err := service.ConfigureArtifactStore(store, ArtifactPluginVerifierFunc(func(_ context.Context, _ plugins.RuntimeIdentity, _ plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, plugins.ErrRuntimeIdentityNotFound
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	session, decision, err := service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSession decision=%+v err=%v", decision, err)
	}
	subject := "logical-retained-parent"
	service.sessionsMu.Lock()
	service.sessions[session.SessionID].scope = sessionScopeState{
		durable: true, subject: subject, status: sessionScopeAttached, version: 1,
	}
	service.sessionsMu.Unlock()
	raw := dispatchRPC(t, service, MethodRetainedBind, RetainedBindParams{
		SessionID: session.SessionID, ControllerToken: session.controllerToken,
	})
	var binding RetainedBindResult
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.BindingToken == "" || binding.ParentSessionID != subject || binding.AccountID != "session:"+subject {
		t.Fatalf("binding=%+v", binding)
	}
	return retainedRPCFixture{service: service, session: session, token: binding.BindingToken, subject: subject}
}

func retainedRPCCall(t *testing.T, fixture retainedRPCFixture, operation, requestID string, payload any, result any) json.RawMessage {
	t.Helper()
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := dispatchRPC(t, fixture.service, MethodRetainedCall, RetainedCallParams{
		BindingToken: fixture.token, RequestID: requestID, Operation: operation, Payload: payloadRaw,
	})
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

func TestRetainedRPCBrokerOwnsLifecycleBudgetMailboxAndReplay(t *testing.T) {
	fixture := newRetainedRPCFixture(t)
	childID := uuid.NewString()
	policy := retained.RestartPolicy{
		Mode: "on_transient_failure", MaxRestarts: 1, Window: 10 * time.Minute,
		BaseBackoff: 250 * time.Millisecond, MaxBackoff: 10 * time.Second,
	}
	admit := RetainedAdmitRequest{
		ChildSessionID: childID, Purpose: "worker",
		Fork: retained.ForkPoint{
			SourceSessionID: fixture.subject, SourceGeneration: 1, CommittedTurn: 2,
			ConversationDigest: strings.Repeat("a", 64), TreeCommit: "tree", TraceCommit: "trace",
			ResolvedAt: time.Now().UTC(),
		},
		CeilingDigest: strings.Repeat("b", 64), Model: "test-model", ToolProfile: "worker_safe",
		Budget: brokerbudget.Limits{Tokens: 100, Turns: 4, WallSeconds: 60}, RestartPolicy: policy,
	}
	var admission retained.Admission
	first := retainedRPCCall(t, fixture, "admit", "spawn-1", admit, &admission)
	if admission.ParentSessionID != fixture.subject || admission.ChildSessionID != childID || admission.Status != retained.StatusAdmitted {
		t.Fatalf("admission=%+v", admission)
	}
	if replay := retainedRPCCall(t, fixture, "admit", "spawn-1", admit, nil); string(replay) != string(first) {
		t.Fatalf("admission replay mismatch: %s != %s", replay, first)
	}

	var running retained.Admission
	retainedRPCCall(t, fixture, "start", "start-1", RetainedStartRequest{
		AdmissionID: admission.ID, Generation: admission.Generation,
		RuntimeNonce: admission.RuntimeNonce, LeaseTTLMS: int64(time.Minute / time.Millisecond),
	}, &running)
	if running.Status != retained.StatusRunning || running.LeaseEpoch == 0 {
		t.Fatalf("running=%+v", running)
	}

	var failed RetainedFinishResult
	retainedRPCCall(t, fixture, "finish", "finish-1", RetainedFinishRequest{
		AdmissionID: running.ID, Generation: running.Generation, LeaseEpoch: running.LeaseEpoch,
		Usage: brokerbudget.Limits{Turns: 1}, Transient: true, Error: "temporary provider outage",
		RestartPolicy: policy,
	}, &failed)
	if !failed.Restart || failed.Admission.Status != retained.StatusDown || failed.BackoffMS != 250 {
		t.Fatalf("failed=%+v", failed)
	}

	var restarted retained.Admission
	retainedRPCCall(t, fixture, "restart", "restart-1", RetainedAdmissionRequest{
		AdmissionID: failed.Admission.ID, Generation: failed.Admission.Generation,
	}, &restarted)
	if restarted.Generation != 2 || restarted.Status != retained.StatusAdmitted {
		t.Fatalf("restarted=%+v", restarted)
	}
	var runningAgain retained.Admission
	retainedRPCCall(t, fixture, "start", "start-2", RetainedStartRequest{
		AdmissionID: restarted.ID, Generation: restarted.Generation,
		RuntimeNonce: restarted.RuntimeNonce, LeaseTTLMS: int64(time.Minute / time.Millisecond),
	}, &runningAgain)

	var sent mailbox.Message
	retainedRPCCall(t, fixture, "followup", "followup-1", RetainedFollowUpRequest{
		AdmissionID: runningAgain.ID, Generation: runningAgain.Generation,
		Payload: json.RawMessage(`{"prompt":"continue"}`),
	}, &sent)
	if sent.SenderSession != fixture.subject || sent.ReceiverSession != childID {
		t.Fatalf("followup=%+v", sent)
	}
	var delivered RetainedDeliverResult
	retainedRPCCall(t, fixture, "deliver", "deliver-child-1", RetainedDeliverRequest{
		ReceiverSession: childID, SenderSession: fixture.subject,
	}, &delivered)
	if !delivered.Found || delivered.Message.ID != sent.ID {
		t.Fatalf("delivered=%+v", delivered)
	}
	var replayed RetainedDeliverResult
	retainedRPCCall(t, fixture, "deliver", "deliver-child-1", RetainedDeliverRequest{
		ReceiverSession: childID, SenderSession: fixture.subject,
	}, &replayed)
	if !replayed.Found || replayed.Message.ID != sent.ID || replayed.Message.DeliveryGeneration != delivered.Message.DeliveryGeneration {
		t.Fatalf("delivery replay=%+v first=%+v", replayed, delivered)
	}
	retainedRPCCall(t, fixture, "ack", "ack-child-1", RetainedAckRequest{
		ReceiverSession: childID, MessageID: sent.ID,
		DeliveryGeneration: delivered.Message.DeliveryGeneration, InputID: "input-1",
	}, nil)

	var completed RetainedFinishResult
	retainedRPCCall(t, fixture, "finish", "finish-2", RetainedFinishRequest{
		AdmissionID: runningAgain.ID, Generation: runningAgain.Generation, LeaseEpoch: runningAgain.LeaseEpoch,
		Usage: brokerbudget.Limits{Turns: 1}, FinalText: "done", RestartPolicy: policy,
	}, &completed)
	if completed.Restart || completed.Admission.Status != retained.StatusCompleted {
		t.Fatalf("completed=%+v", completed)
	}
	var finishReplay RetainedFinishResult
	retainedRPCCall(t, fixture, "finish", "finish-2", RetainedFinishRequest{
		AdmissionID: runningAgain.ID, Generation: runningAgain.Generation, LeaseEpoch: runningAgain.LeaseEpoch,
		Usage: brokerbudget.Limits{Turns: 1}, FinalText: "done", RestartPolicy: policy,
	}, &finishReplay)
	if finishReplay.Admission.Status != retained.StatusCompleted {
		t.Fatalf("finish replay=%+v", finishReplay)
	}
	var reply RetainedDeliverResult
	retainedRPCCall(t, fixture, "deliver", "deliver-parent-1", RetainedDeliverRequest{
		ReceiverSession: fixture.subject, SenderSession: childID,
	}, &reply)
	if !reply.Found || string(reply.Message.Payload) != `{"text":"done"}` {
		t.Fatalf("reply=%+v", reply)
	}

	var listed []retained.Admission
	retainedRPCCall(t, fixture, "list", "list-1", struct{}{}, &listed)
	if len(listed) != 1 || listed[0].ID != admission.ID || listed[0].Generation != 2 {
		t.Fatalf("listed=%+v", listed)
	}
}

func TestRetainedRPCRejectsCallerAuthorityAndStaleBinding(t *testing.T) {
	fixture := newRetainedRPCFixture(t)
	payload := json.RawMessage(`{
		"child_session_id":"` + uuid.NewString() + `",
		"parent_session_id":"forged",
		"purpose":"worker",
		"fork_point":{},
		"ceiling_digest":"` + strings.Repeat("a", 64) + `",
		"budget":{"tokens":1,"turns":1,"wall_seconds":1}
	}`)
	raw, err := json.Marshal(RetainedCallParams{
		BindingToken: fixture.token, RequestID: "forged", Operation: "admit", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Dispatch(t.Context(), MethodRetainedCall, raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("caller-selected parent reached retained authority: %v", err)
	}

	fixture.service.sessionsMu.Lock()
	fixture.service.sessions[fixture.session.SessionID].controllerVersion++
	fixture.service.sessionsMu.Unlock()
	payload, _ = json.Marshal(struct{}{})
	raw, _ = json.Marshal(RetainedCallParams{
		BindingToken: fixture.token, RequestID: "stale", Operation: "list", Payload: payload,
	})
	if _, err := fixture.service.Dispatch(t.Context(), MethodRetainedCall, raw); err == nil || !strings.Contains(err.Error(), "stale retained binding") {
		t.Fatalf("rotated controller retained authority: %v", err)
	}
}

func TestRetainedBindRequiresDurableLogicalSession(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(DefaultPolicy(), nil)
	if err := service.ConfigureArtifactStore(store, ArtifactPluginVerifierFunc(func(_ context.Context, _ plugins.RuntimeIdentity, _ plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		return plugins.RuntimeIdentity{}, plugins.Manifest{}, plugins.ErrRuntimeIdentityNotFound
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	session, decision, err := service.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir()})
	if err != nil || !decision.Admit {
		t.Fatalf("session decision=%+v err=%v", decision, err)
	}
	raw, _ := json.Marshal(RetainedBindParams{SessionID: session.SessionID, ControllerToken: session.controllerToken})
	if _, err := service.Dispatch(t.Context(), MethodRetainedBind, raw); err == nil || !strings.Contains(err.Error(), "durable logical session") {
		t.Fatalf("ephemeral session retained binding error=%v", err)
	}
}
