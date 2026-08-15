package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

type lifecycleRPCFixture struct {
	service  *Service
	session  SessionHandle
	manifest plugins.Manifest
	identity plugins.RuntimeIdentity
}

func newLifecycleRPCFixture(t *testing.T, events []string, capabilities ...string) lifecycleRPCFixture {
	t.Helper()
	manifest := plugins.Manifest{
		Name: "watcher", Version: "v1.0.0",
		Lifecycle:    &plugins.LifecycleDef{Events: append([]string(nil), events...)},
		Capabilities: append([]string(nil), capabilities...),
		Tools:        []plugins.ToolDef{{Name: "watcher__status"}},
	}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(DefaultPolicy(), nil)
	verifier := ArtifactPluginVerifierFunc(func(_ context.Context, requested plugins.RuntimeIdentity, _ plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		if requested != identity {
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, plugins.ErrRuntimeIdentityNotFound
		}
		// Return the trust-source copy, never the caller's manifest. The tests
		// deliberately submit a manifest with an extra unsigned subscription.
		return identity, manifest, nil
	})
	if err := service.ConfigureArtifactStore(store, verifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	session, decision, err := service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSession handle=%+v decision=%+v err=%v", session, decision, err)
	}
	return lifecycleRPCFixture{service: service, session: session, manifest: manifest, identity: identity}
}

func dispatchRPC(t *testing.T, service *Service, method string, params any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Dispatch(context.Background(), method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}

func bindLifecycleRPC(t *testing.T, fixture lifecycleRPCFixture, requested plugins.Manifest) ApplicationBindResult {
	t.Helper()
	raw := dispatchRPC(t, fixture.service, MethodApplicationBind, ApplicationBindParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		Identity: fixture.identity, Manifest: requested,
	})
	var result ApplicationBindResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.BindingToken == "" {
		t.Fatal("broker returned an empty application binding")
	}
	return result
}

func publishLifecycleRPC(t *testing.T, fixture lifecycleRPCFixture, id, kind string) ApplicationEventResult {
	t.Helper()
	raw := dispatchRPC(t, fixture.service, MethodApplicationEventPublish, ApplicationEventPublishParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		ExpectedGeneration: 1,
		RequestID:          "publish:" + id,
		ID:                 id, Kind: kind, Data: json.RawMessage(`{"id":"` + id + `"}`),
	})
	var result ApplicationEventResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func nextLifecycleRPC(t *testing.T, service *Service, token string, limit int) ([]ApplicationEventResult, json.RawMessage) {
	t.Helper()
	raw := dispatchRPC(t, service, MethodApplicationEventsNext, ApplicationEventsNextParams{BindingToken: token, Limit: limit})
	var events []ApplicationEventResult
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	return events, raw
}

func TestApplicationEventRPCUsesVerifiedSubscriptionsAndDurableCursor(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"session.turn_committed"}, "lifecycle:observe:session.turn_committed")
	requested := fixture.manifest
	requested.Lifecycle = &plugins.LifecycleDef{Events: []string{"session.turn_committed", "session.unsigned"}}
	requested.Capabilities = append(requested.Capabilities, "lifecycle:observe:session.unsigned")
	binding := bindLifecycleRPC(t, fixture, requested)

	fixture.service.artifacts.mu.RLock()
	admitted := append([]string(nil), fixture.service.artifacts.bindings[binding.BindingToken].eventKinds...)
	fixture.service.artifacts.mu.RUnlock()
	if len(admitted) != 1 || admitted[0] != "session.turn_committed" {
		t.Fatalf("broker admitted caller-selected subscriptions: %v", admitted)
	}

	first := publishLifecycleRPC(t, fixture, "turn-1", "session.turn_committed")
	unsigned := publishLifecycleRPC(t, fixture, "unsigned-1", "session.unsigned")
	second := publishLifecycleRPC(t, fixture, "turn-2", "session.turn_committed")
	events, raw := nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
	if len(events) != 2 || events[0].WALSequence != first.WALSequence || events[1].WALSequence != second.WALSequence {
		t.Fatalf("pending events=%+v", events)
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &objects); err != nil {
		t.Fatal(err)
	}
	allowedFields := map[string]bool{"kind": true, "wal_sequence": true, "evidence_refs": true, "data": true}
	for _, object := range objects {
		for field := range object {
			if !allowedFields[field] {
				t.Fatalf("internal event field %q escaped on application RPC: %s", field, raw)
			}
		}
	}

	// A caller cannot use the acknowledgement cursor to smuggle an event from
	// outside the signed subscription into the cumulative cursor.
	badAck, _ := json.Marshal(ApplicationEventsAckParams{
		BindingToken: binding.BindingToken, RequestID: "ack-unsigned", Sequence: unsigned.WALSequence,
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsAck, badAck); err == nil || !strings.Contains(err.Error(), "signed subscriptions") {
		t.Fatalf("unsigned event acknowledgement error=%v", err)
	}

	ack := func(requestID string, sequence uint64) json.RawMessage {
		t.Helper()
		return dispatchRPC(t, fixture.service, MethodApplicationEventsAck, ApplicationEventsAckParams{
			BindingToken: binding.BindingToken, RequestID: requestID, Sequence: sequence,
		})
	}
	ack("ack-turn-1", first.WALSequence)
	events, _ = nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
	if len(events) != 1 || events[0].WALSequence != second.WALSequence {
		t.Fatalf("post-first-ack events=%+v", events)
	}
	committed := ack("ack-turn-2", second.WALSequence)
	retry := ack("ack-turn-2", second.WALSequence)
	if string(committed) != string(retry) {
		t.Fatalf("idempotent ack mismatch: %s != %s", committed, retry)
	}
	events, _ = nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
	if len(events) != 0 {
		t.Fatalf("acknowledged events replayed: %+v", events)
	}
}

func TestLifecycleApplicationBindingRetainsPackageWideCapabilities(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil, "session:schedule")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	fixture.service.artifacts.mu.RLock()
	state := fixture.service.artifacts.bindings[binding.BindingToken]
	fixture.service.artifacts.mu.RUnlock()
	if !state.lifecycle || !state.hasCapability("session:schedule") {
		t.Fatalf("lifecycle package authority was attenuated: lifecycle=%v caps=%v", state.lifecycle, state.capabilities)
	}

	raw, err := json.Marshal(map[string]any{
		"session_id": fixture.session.SessionID, "controller_token": fixture.session.controllerToken,
		"identity": fixture.identity, "manifest": fixture.manifest, "tool_name": fixture.manifest.Tools[0].Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationBind, raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("application bind accepted ordinary tool selector: %v", err)
	}
}

func TestLifecycleBindingTokenCarriesExactEvidenceAuthorityAndFences(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil, "evidence:catalog:artifact")
	fixture.service.sessionsMu.Lock()
	fixture.service.sessions[fixture.session.SessionID].scope.durable = true
	fixture.service.sessions[fixture.session.SessionID].scope.subject = "logical-session"
	fixture.service.sessionsMu.Unlock()
	first := bindLifecycleRPC(t, fixture, fixture.manifest)
	call := func(token string, payload json.RawMessage) error {
		_, err := fixture.service.evidenceCall(context.Background(), EvidenceCallParams{
			BindingToken: token, Operation: "catalog", Payload: payload,
		})
		return err
	}
	if err := call(first.BindingToken, json.RawMessage(`{"corpus":"artifact"}`)); err != nil {
		t.Fatalf("declared lifecycle evidence capability failed: %v", err)
	}
	if err := call(first.BindingToken, json.RawMessage(`{"corpus":"artifact","session_id":"other"}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("guest-selected evidence session reached the broker: %v", err)
	}

	undeclared := newLifecycleRPCFixture(t, nil)
	denied := bindLifecycleRPC(t, undeclared, undeclared.manifest)
	if _, err := undeclared.service.evidenceCall(context.Background(), EvidenceCallParams{
		BindingToken: denied.BindingToken, Operation: "catalog", Payload: json.RawMessage(`{"corpus":"artifact"}`),
	}); err == nil || !strings.Contains(err.Error(), "evidence:catalog:artifact capability required") {
		t.Fatalf("undeclared lifecycle evidence capability was accepted: %v", err)
	}

	second := bindLifecycleRPC(t, fixture, fixture.manifest)
	if err := call(first.BindingToken, json.RawMessage(`{"corpus":"artifact"}`)); err == nil || !strings.Contains(err.Error(), "unknown artifact binding") {
		t.Fatalf("superseded lifecycle evidence token remained active: %v", err)
	}
	if err := call(second.BindingToken, json.RawMessage(`{"corpus":"artifact"}`)); err != nil {
		t.Fatalf("rebound lifecycle evidence token failed: %v", err)
	}
	fixture.service.sessionsMu.Lock()
	fixture.service.sessions[fixture.session.SessionID].generation++
	fixture.service.sessionsMu.Unlock()
	if err := call(second.BindingToken, json.RawMessage(`{"corpus":"artifact"}`)); err == nil || !strings.Contains(err.Error(), "stale artifact binding") {
		t.Fatalf("generation-stale lifecycle evidence token remained active: %v", err)
	}
}

func TestApplicationContextReadUsesHiddenBindingSubjectAndEmptyRequest(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil, "session:context:read")
	fixture.service.sessionsMu.Lock()
	fixture.service.sessions[fixture.session.SessionID].scope.subject = "logical-current-session"
	fixture.service.sessionsMu.Unlock()
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)

	fixture.service.artifacts.mu.RLock()
	authority := fixture.service.artifacts.bindings[binding.BindingToken].applicationAuthority()
	fixture.service.artifacts.mu.RUnlock()
	if authority.Subject != "logical-current-session" {
		t.Fatalf("application authority subject = %q", authority.Subject)
	}

	raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "context:read", Operation: "context.read",
		Payload: json.RawMessage(`{}`),
	})
	var snapshot application.ContextSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.Schema != "stado.dev/session-context-facts/v1" {
		t.Fatalf("context snapshot=%s parsed=%+v err=%v", raw, snapshot, err)
	}
	if strings.Contains(string(raw), "logical-current-session") {
		t.Fatalf("hidden logical subject leaked into response: %s", raw)
	}
	for _, forged := range []string{
		`{"subject":"other"}`, `{"session_id":"other"}`, `{"generation":99}`,
		`{"path":"/tmp/foreign"}`, `{"plugin":"github.com/other/app"}`,
	} {
		params, _ := json.Marshal(ApplicationCallParams{
			BindingToken: binding.BindingToken, RequestID: "context:forged", Operation: "context.read",
			Payload: json.RawMessage(forged),
		})
		if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, params); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("forged context selector %s error=%v", forged, err)
		}
	}
}

func TestApplicationCompletionRPCRequiresExactCapabilityAndProjectsSchedule(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil, "session:complete")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "complete:run-1", Operation: "session.complete",
		Payload: json.RawMessage(`{"run_id":"run-1","summary":"finished","evidence_refs":["journal:7"]}`),
	})
	var completion struct {
		RunID       string `json:"run_id"`
		WALSequence uint64 `json:"wal_sequence"`
	}
	if err := json.Unmarshal(raw, &completion); err != nil || completion.RunID != "run-1" || completion.WALSequence == 0 {
		t.Fatalf("completion=%s err=%v", raw, err)
	}
	projection, err := fixture.service.sessionSchedule(context.Background(), fixture.session.SessionID, fixture.session.controllerToken)
	if err != nil || projection.LatestCompletion == nil || projection.LatestCompletion.WALSequence != completion.WALSequence {
		t.Fatalf("schedule projection=%+v err=%v", projection, err)
	}
	forged, _ := json.Marshal(ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "complete:forged", Operation: "session.complete",
		Payload: json.RawMessage(`{"run_id":"run-2","session_id":"other","generation":99}`),
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, forged); err == nil {
		t.Fatal("completion payload selected broker authority")
	}

	withoutCap := newLifecycleRPCFixture(t, nil, "session:schedule")
	withoutCapBinding := bindLifecycleRPC(t, withoutCap, withoutCap.manifest)
	payload, _ := json.Marshal(ApplicationCallParams{
		BindingToken: withoutCapBinding.BindingToken, RequestID: "complete", Operation: "session.complete",
		Payload: json.RawMessage(`{"run_id":"run-1"}`),
	})
	if _, err := withoutCap.service.Dispatch(context.Background(), MethodApplicationCall, payload); err == nil || !strings.Contains(err.Error(), "session:complete") {
		t.Fatalf("missing exact completion capability error=%v", err)
	}
}

func TestApplicationWorkerRunRPCUsesExactCapabilitiesAndDurableScheduleConsumption(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil,
		"session:worker:request", "session:worker:resume", "session:worker:cancel", "session:schedule")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	call := func(requestID, operation, payload string, out any) {
		t.Helper()
		raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
			BindingToken: binding.BindingToken, RequestID: requestID,
			Operation: operation, Payload: json.RawMessage(payload),
		})
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}

	var requested application.WorkerRun
	call("worker:request", "worker.request", `{
		"run_id":"run-1","objective":"finish the task","prompt":"continue carefully","conflict":"reject"
	}`, &requested)
	if requested.Status != application.WorkerRunRequested || requested.PluginID != fixture.identity.Namespace || requested.SessionID != fixture.session.SessionID {
		t.Fatalf("requested worker run = %+v", requested)
	}
	for _, operation := range []string{"worker.get", "worker.activate", "worker.resume.activate", "worker.cancel-host"} {
		hidden, _ := json.Marshal(ApplicationCallParams{
			BindingToken: binding.BindingToken, RequestID: "hidden:" + operation, Operation: operation,
			Payload: json.RawMessage(`{"run_id":"run-1","expected_version":1,"reason":"hidden"}`),
		})
		if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, hidden); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("application bearer reached native operation %q: %v", operation, err)
		}
	}
	var fetched application.WorkerRun
	fetchedRaw := dispatchRPC(t, fixture.service, MethodApplicationWorkerGet, ApplicationWorkerGetParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: requested.RunID,
	})
	if err := json.Unmarshal(fetchedRaw, &fetched); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requested, fetched) {
		t.Fatalf("native get = %+v, want %+v", fetched, requested)
	}
	var active application.WorkerRun
	activeRaw := dispatchRPC(t, fixture.service, MethodApplicationWorkerActivate, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: requested.RunID, ExpectedVersion: requested.Version,
	})
	if err := json.Unmarshal(activeRaw, &active); err != nil {
		t.Fatal(err)
	}
	if active.Status != application.WorkerRunActive || active.Version != 2 {
		t.Fatalf("active worker run = %+v", active)
	}

	var pause application.ControlRequest
	call("pause", "session.pause", `{"run_id":"run-1","reason_code":"operator-input","reason":"operator input required"}`, &pause)
	projection, err := fixture.service.sessionSchedule(context.Background(), fixture.session.SessionID, fixture.session.controllerToken)
	if err != nil || projection.ActiveWorkerRun == nil || projection.LatestPause == nil {
		t.Fatalf("pre-consume projection = %+v, %v", projection, err)
	}
	raw := dispatchRPC(t, fixture.service, MethodSessionScheduleConsume, SessionScheduleConsumeParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		Sequence: pause.WALSequence,
	})
	var consumed SessionScheduleResult
	if err := json.Unmarshal(raw, &consumed); err != nil {
		t.Fatal(err)
	}
	if consumed.ActiveWorkerRun != nil || consumed.LatestWorkerRun == nil || consumed.LatestWorkerRun.Status != application.WorkerRunInterrupted || consumed.LatestWorkerRun.TerminalSequence != pause.WALSequence {
		t.Fatalf("consumed projection = %+v", consumed)
	}
	// Retrying the native consumption is harmless after the run is terminal.
	dispatchRPC(t, fixture.service, MethodSessionScheduleConsume, SessionScheduleConsumeParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		Sequence: pause.WALSequence,
	})

	var resumeRequested application.WorkerRun
	call("worker:resume", "worker.resume", fmt.Sprintf(`{"run_id":"run-1","expected_version":%d}`, consumed.LatestWorkerRun.Version), &resumeRequested)
	if resumeRequested.Status != application.WorkerRunResumeRequested || resumeRequested.RunID != requested.RunID || resumeRequested.TerminalSequence != pause.WALSequence {
		t.Fatalf("resume request = %+v", resumeRequested)
	}
	resumedRaw := dispatchRPC(t, fixture.service, MethodApplicationWorkerResumeActivate, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: requested.RunID, ExpectedVersion: resumeRequested.Version,
	})
	var resumed application.WorkerRun
	if err := json.Unmarshal(resumedRaw, &resumed); err != nil || resumed.Status != application.WorkerRunActive || resumed.Version != resumeRequested.Version+1 ||
		resumed.SessionID != requested.SessionID || resumed.Generation != requested.Generation || resumed.PluginID != requested.PluginID || resumed.RunID != requested.RunID {
		t.Fatalf("resumed worker = %s, %v", resumedRaw, err)
	}
	resumedReplay := dispatchRPC(t, fixture.service, MethodApplicationWorkerResumeActivate, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: requested.RunID, ExpectedVersion: resumeRequested.Version,
	})
	if !reflect.DeepEqual(resumedRaw, resumedReplay) {
		t.Fatalf("native resume replay = %s, want %s", resumedReplay, resumedRaw)
	}

	forged, _ := json.Marshal(ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "worker:forged", Operation: "worker.request",
		Payload: json.RawMessage(`{"run_id":"run-2","objective":"x","prompt":"x","conflict":"reject","plugin_id":"evil","session_id":"other"}`),
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, forged); err == nil {
		t.Fatal("worker request payload selected broker authority")
	}
	unauthenticated, _ := json.Marshal(SessionScheduleConsumeParams{
		SessionID: fixture.session.SessionID, ControllerToken: "controller_wrong", Sequence: pause.WALSequence,
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodSessionScheduleConsume, unauthenticated); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("unauthenticated schedule consumption error = %v", err)
	}

	withoutCap := newLifecycleRPCFixture(t, nil, "session:schedule")
	withoutCapBinding := bindLifecycleRPC(t, withoutCap, withoutCap.manifest)
	missingCap, _ := json.Marshal(ApplicationCallParams{
		BindingToken: withoutCapBinding.BindingToken, RequestID: "worker:request", Operation: "worker.request",
		Payload: json.RawMessage(`{"run_id":"run","objective":"x","prompt":"x","conflict":"reject"}`),
	})
	if _, err := withoutCap.service.Dispatch(context.Background(), MethodApplicationCall, missingCap); err == nil || !strings.Contains(err.Error(), "session:worker:request") {
		t.Fatalf("missing exact worker capability error = %v", err)
	}
	missingResume, _ := json.Marshal(ApplicationCallParams{
		BindingToken: withoutCapBinding.BindingToken, RequestID: "worker:resume", Operation: "worker.resume",
		Payload: json.RawMessage(`{"run_id":"run","expected_version":3}`),
	})
	if _, err := withoutCap.service.Dispatch(context.Background(), MethodApplicationCall, missingResume); err == nil || !strings.Contains(err.Error(), "session:worker:resume") {
		t.Fatalf("missing exact resume capability error = %v", err)
	}
	nativeMissingResume, _ := json.Marshal(ApplicationWorkerTransitionParams{
		SessionID: withoutCap.session.SessionID, ControllerToken: withoutCap.session.controllerToken,
		BindingToken: withoutCapBinding.BindingToken, RunID: "run", ExpectedVersion: 3,
	})
	if _, err := withoutCap.service.Dispatch(context.Background(), MethodApplicationWorkerResumeActivate, nativeMissingResume); err == nil || !strings.Contains(err.Error(), "session:worker:resume") {
		t.Fatalf("native resume activation ignored exact capability: %v", err)
	}
}

func TestApplicationWorkerHostCancelCannotBeDisabledByGuestCapabilityChoice(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil, "session:worker:request")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	raw := dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "request", Operation: "worker.request",
		Payload: json.RawMessage(`{"run_id":"run","objective":"finish","prompt":"continue","conflict":"reject"}`),
	})
	var requested application.WorkerRun
	if err := json.Unmarshal(raw, &requested); err != nil {
		t.Fatal(err)
	}
	guestCancel, _ := json.Marshal(ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "guest-cancel", Operation: "worker.cancel",
		Payload: json.RawMessage(`{"run_id":"run","expected_version":1,"reason":"guest"}`),
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, guestCancel); err == nil || !strings.Contains(err.Error(), "session:worker:cancel") {
		t.Fatalf("guest cancel without capability error = %v", err)
	}
	hostCancel := dispatchRPC(t, fixture.service, MethodApplicationWorkerCancel, ApplicationWorkerTransitionParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		BindingToken: binding.BindingToken, RunID: "run", ExpectedVersion: 1,
		Reason: "operator stopped recurrence",
	})
	var cancelled application.WorkerRun
	if err := json.Unmarshal(hostCancel, &cancelled); err != nil || cancelled.Status != application.WorkerRunCancelled || requested.RunID != cancelled.RunID {
		t.Fatalf("host cancel = %s, %v", hostCancel, err)
	}
}

func TestApplicationEventRPCTimerPromotionIsTargetedAndAcked(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"timer.due"},
		"lifecycle:observe:timer.due", "timer:schedule")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	dueAt := time.Now().UTC().Add(100 * time.Millisecond)
	payload, err := json.Marshal(map[string]any{
		"id": "wake-1", "run_id": "run-1", "name": "watchdog.poll",
		"due_at": dueAt, "payload": map[string]any{"attempt": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatchRPC(t, fixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "schedule-wake-1",
		Operation: "timer.schedule", Payload: payload,
	})

	deadline := time.Now().Add(3 * time.Second)
	var events []ApplicationEventResult
	for len(events) == 0 && time.Now().Before(deadline) {
		events, _ = nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
		if len(events) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(events) != 1 || events[0].Kind != "timer.due" || events[0].WALSequence == 0 || !strings.Contains(string(events[0].Data), `"id":"wake-1"`) || !strings.Contains(string(events[0].Data), `"status":"due"`) {
		t.Fatalf("promoted timer events=%+v", events)
	}
	replayed, _ := nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
	if len(replayed) != 1 || replayed[0].WALSequence != events[0].WALSequence {
		t.Fatalf("unacked timer was not replayed: %+v", replayed)
	}
	dispatchRPC(t, fixture.service, MethodApplicationEventsAck, ApplicationEventsAckParams{
		BindingToken: binding.BindingToken, RequestID: "ack-wake-1", Sequence: events[0].WALSequence,
	})
	events, _ = nextLifecycleRPC(t, fixture.service, binding.BindingToken, 10)
	if len(events) != 0 {
		t.Fatalf("acked timer replayed: %+v", events)
	}
}

func TestApplicationEventRPCRejectsWrongStaleAndUnknownBindings(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"session.turn_committed"}, "lifecycle:observe:session.turn_committed")
	firstBinding := bindLifecycleRPC(t, fixture, fixture.manifest)
	secondSession, decision, err := fixture.service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("second session=%+v decision=%+v err=%v", secondSession, decision, err)
	}
	secondFixture := fixture
	secondFixture.session = secondSession
	secondBinding := bindLifecycleRPC(t, secondFixture, fixture.manifest)
	published := publishLifecycleRPC(t, fixture, "turn-1", "session.turn_committed")

	events, _ := nextLifecycleRPC(t, fixture.service, secondBinding.BindingToken, 10)
	if len(events) != 0 {
		t.Fatalf("wrong-session binding received events: %+v", events)
	}
	wrongAck, _ := json.Marshal(ApplicationEventsAckParams{
		BindingToken: secondBinding.BindingToken, RequestID: "wrong-session", Sequence: published.WALSequence,
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsAck, wrongAck); err == nil {
		t.Fatal("wrong-session binding advanced the event cursor")
	}
	unknown, _ := json.Marshal(ApplicationEventsNextParams{BindingToken: "artifact_unknown", Limit: 10})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsNext, unknown); err == nil || !strings.Contains(err.Error(), "unknown artifact binding") {
		t.Fatalf("unknown binding error=%v", err)
	}

	// Model a restored session incarnation while retaining an old in-memory
	// token: generation mismatch must independently invalidate the binding.
	fixture.service.sessionsMu.Lock()
	fixture.service.sessions[fixture.session.SessionID].generation++
	fixture.service.sessionsMu.Unlock()
	stale, _ := json.Marshal(ApplicationEventsNextParams{BindingToken: firstBinding.BindingToken, Limit: 10})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsNext, stale); err == nil || !strings.Contains(err.Error(), "stale artifact binding") {
		t.Fatalf("stale binding error=%v", err)
	}
}

func TestApplicationEventRPCRejectsGuestSelectedAuthorityAndSubscriptions(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"session.turn_committed"},
		"lifecycle:observe:session.turn_committed", "session:projection:read")
	binding := bindLifecycleRPC(t, fixture, fixture.manifest)
	for name, raw := range map[string]string{
		"event kinds":    `{"binding_token":"` + binding.BindingToken + `","event_kinds":["session.secret"]}`,
		"session":        `{"binding_token":"` + binding.BindingToken + `","session_id":"other"}`,
		"plugin":         `{"binding_token":"` + binding.BindingToken + `","plugin_id":"evil"}`,
		"cursor":         `{"binding_token":"` + binding.BindingToken + `","cursor":999}`,
		"binding fields": `{"binding_token":"` + binding.BindingToken + `","generation":99}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationEventsNext, json.RawMessage(raw)); err == nil {
				t.Fatalf("guest-selected %s was accepted", name)
			}
		})
	}

	call, _ := json.Marshal(ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "forged-authority", Operation: "projection.read",
		Payload: json.RawMessage(`{"session_id":"other","generation":99,"plugin_id":"evil"}`),
	})
	if _, err := fixture.service.Dispatch(context.Background(), MethodApplicationCall, call); err == nil {
		t.Fatal("guest authority in application payload was accepted")
	}
}

func TestNativeLifecycleRPCRequiresExactSessionController(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"session.turn_committed"},
		"lifecycle:observe:session.turn_committed")

	rejected := func(method string, params any) {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.Dispatch(context.Background(), method, raw); err == nil ||
			(!strings.Contains(err.Error(), "controller") && !strings.Contains(err.Error(), "controller_token")) {
			t.Fatalf("%s accepted an unauthenticated native caller: %v", method, err)
		}
	}

	for _, token := range []string{"", "controller_wrong"} {
		rejected(MethodArtifactBind, ArtifactBindParams{
			SessionID: fixture.session.SessionID, ControllerToken: token,
			Identity: fixture.identity, Manifest: fixture.manifest,
		})
		rejected(MethodApplicationBind, ApplicationBindParams{
			SessionID: fixture.session.SessionID, ControllerToken: token,
			Identity: fixture.identity, Manifest: fixture.manifest,
		})
		rejected(MethodApplicationEventPublish, ApplicationEventPublishParams{
			SessionID: fixture.session.SessionID, ControllerToken: token,
			ExpectedGeneration: 1,
			RequestID:          "unauthenticated", Kind: "session.turn_committed", Data: json.RawMessage(`{}`),
		})
		rejected(MethodSessionSchedule, SessionScheduleParams{
			SessionID: fixture.session.SessionID, ControllerToken: token,
		})
	}

	workerFixture := newLifecycleRPCFixture(t, nil, "session:worker:request")
	workerBinding := bindLifecycleRPC(t, workerFixture, workerFixture.manifest)
	workerRaw := dispatchRPC(t, workerFixture.service, MethodApplicationCall, ApplicationCallParams{
		BindingToken: workerBinding.BindingToken, RequestID: "request", Operation: "worker.request",
		Payload: json.RawMessage(`{"run_id":"run","objective":"finish","prompt":"continue","conflict":"reject"}`),
	})
	var worker application.WorkerRun
	if err := json.Unmarshal(workerRaw, &worker); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "controller_wrong"} {
		rejectedWorker := func(method string, params any) {
			t.Helper()
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workerFixture.service.Dispatch(context.Background(), method, raw); err == nil ||
				(!strings.Contains(err.Error(), "controller") && !strings.Contains(err.Error(), "controller_token")) {
				t.Fatalf("%s accepted an unauthenticated worker controller: %v", method, err)
			}
		}
		rejectedWorker(MethodApplicationWorkerGet, ApplicationWorkerGetParams{
			SessionID: workerFixture.session.SessionID, ControllerToken: token,
			BindingToken: workerBinding.BindingToken, RunID: worker.RunID,
		})
		rejectedWorker(MethodApplicationWorkerActivate, ApplicationWorkerTransitionParams{
			SessionID: workerFixture.session.SessionID, ControllerToken: token,
			BindingToken: workerBinding.BindingToken, RunID: worker.RunID, ExpectedVersion: worker.Version,
		})
		rejectedWorker(MethodApplicationWorkerCancel, ApplicationWorkerTransitionParams{
			SessionID: workerFixture.session.SessionID, ControllerToken: token,
			BindingToken: workerBinding.BindingToken, RunID: worker.RunID, ExpectedVersion: worker.Version, Reason: "stop",
		})
	}

	other, decision, err := fixture.service.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("second session=%+v decision=%+v err=%v", other, decision, err)
	}
	rejected(MethodApplicationEventPublish, ApplicationEventPublishParams{
		SessionID: fixture.session.SessionID, ControllerToken: other.controllerToken,
		ExpectedGeneration: 1,
		RequestID:          "cross-session", Kind: "session.turn_committed", Data: json.RawMessage(`{}`),
	})

	published := publishLifecycleRPC(t, fixture, "authenticated", "session.turn_committed")
	if published.WALSequence == 0 {
		t.Fatal("authenticated event was not published")
	}
	records, err := json.Marshal(fixture.service.artifacts.store.Records())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(records), fixture.session.controllerToken) {
		t.Fatal("plaintext controller token entered the broker WAL or event payload")
	}
}

func TestLifecycleRebindIsExclusiveAndKeepsDurableCursor(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, []string{"session.turn_committed"},
		"lifecycle:observe:session.turn_committed")
	firstBinding := bindLifecycleRPC(t, fixture, fixture.manifest)
	first := publishLifecycleRPC(t, fixture, "turn-before-rebind", "session.turn_committed")
	second := publishLifecycleRPC(t, fixture, "turn-after-cursor", "session.turn_committed")
	dispatchRPC(t, fixture.service, MethodApplicationEventsAck, ApplicationEventsAckParams{
		BindingToken: firstBinding.BindingToken, RequestID: "ack-before-rebind", Sequence: first.WALSequence,
	})

	// An ordinary artifact admission for the same plugin is independent. It
	// neither owns the lifecycle cursor nor supersedes the lifecycle instance.
	artifactBinding, err := fixture.service.bindArtifacts(context.Background(), ArtifactBindParams{
		SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
		Identity: fixture.identity, Manifest: fixture.manifest,
		ToolName: fixture.manifest.Tools[0].Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.applicationBinding(artifactBinding.BindingToken); err == nil || !strings.Contains(err.Error(), "artifact-only") {
		t.Fatalf("artifact-only token acquired lifecycle authority: %v", err)
	}
	if _, err := fixture.service.applicationBinding(firstBinding.BindingToken); err != nil {
		t.Fatalf("artifact admission superseded lifecycle binding: %v", err)
	}

	secondBinding := bindLifecycleRPC(t, fixture, fixture.manifest)
	if secondBinding.BindingToken == firstBinding.BindingToken {
		t.Fatal("lifecycle rebind reused an old bearer")
	}
	if _, err := fixture.service.applicationBinding(firstBinding.BindingToken); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("superseded lifecycle binding remained active: %v", err)
	}
	events, _ := nextLifecycleRPC(t, fixture.service, secondBinding.BindingToken, 10)
	if len(events) != 1 || events[0].WALSequence != second.WALSequence {
		t.Fatalf("durable namespace cursor did not survive rebind: %+v", events)
	}

	fixture.service.artifacts.mu.RLock()
	lifecycleCount := len(fixture.service.artifacts.lifecycleBindings)
	bindingCount := len(fixture.service.artifacts.bindings)
	fixture.service.artifacts.mu.RUnlock()
	if lifecycleCount != 1 || bindingCount != 2 {
		t.Fatalf("binding registry lifecycle=%d total=%d, want one lifecycle plus one artifact", lifecycleCount, bindingCount)
	}
}

func TestConcurrentLifecycleRebindLeavesExactlyOneActiveToken(t *testing.T) {
	fixture := newLifecycleRPCFixture(t, nil)
	const attempts = 16
	tokens := make(chan string, attempts)
	errs := make(chan error, attempts)
	for range attempts {
		go func() {
			binding, err := fixture.service.bindApplication(context.Background(), ApplicationBindParams{
				SessionID: fixture.session.SessionID, ControllerToken: fixture.session.controllerToken,
				Identity: fixture.identity, Manifest: fixture.manifest,
			})
			if err == nil {
				tokens <- binding.BindingToken
			}
			errs <- err
		}()
	}
	var issued []string
	for range attempts {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		issued = append(issued, <-tokens)
	}
	active := 0
	for _, token := range issued {
		if _, err := fixture.service.applicationBinding(token); err == nil {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("concurrent lifecycle rebind left %d active tokens, want 1", active)
	}
}
