package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

func artifactBrokerFixture(t *testing.T) (*Service, SessionHandle, plugins.Manifest, plugins.RuntimeIdentity) {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(DefaultPolicy(), nil)
	verifier := ArtifactPluginVerifierFunc(func(_ context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		return identity, manifest, nil
	})
	if err := svc.ConfigureArtifactStore(store, verifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	handle, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSession handle=%+v decision=%+v err=%v", handle, decision, err)
	}
	manifest := plugins.Manifest{
		Name: "reviewer", Version: "v1.0.0",
		ArtifactKinds: []plugins.ArtifactKindDef{{
			Name:   "finding",
			Schema: `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`,
		}},
		Capabilities: []string{
			"artifact:propose:finding", "artifact:edit:finding",
		},
	}
	localSource := t.TempDir()
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, localSource)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Capabilities = append(manifest.Capabilities,
		"artifact:read:"+identity.Namespace+"#finding",
		"artifact:observe:"+identity.Namespace+"#finding",
	)
	identity, err = plugins.RuntimeIdentityForLocalSource(manifest, localSource)
	if err != nil {
		t.Fatal(err)
	}
	return svc, handle, manifest, identity
}

func bindArtifactFixture(t *testing.T, svc *Service, handle SessionHandle, manifest plugins.Manifest, identity plugins.RuntimeIdentity) ArtifactBindResult {
	t.Helper()
	result, err := svc.bindArtifacts(context.Background(), ArtifactBindParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		Identity: identity, Manifest: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func bindApplicationFixture(t *testing.T, svc *Service, handle SessionHandle, manifest plugins.Manifest, identity plugins.RuntimeIdentity) ApplicationBindResult {
	t.Helper()
	result, err := svc.bindApplication(context.Background(), ApplicationBindParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		Identity: identity, Manifest: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestArtifactBrokerBindsIdentityAndHostScope(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)
	if binding.BindingToken == "" || binding.SessionID != session.SessionID || binding.Principal == "" || binding.SessionGeneration != 1 {
		t.Fatalf("binding=%+v", binding)
	}

	createdRaw, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "create-1",
		Payload: json.RawMessage(`{"kind":"finding","scope":"repo","data":{"summary":"stale verdict"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var created artifacts.Artifact
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		t.Fatal(err)
	}
	wantKind, err := identity.QualifiedKind("finding")
	if err != nil {
		t.Fatal(err)
	}
	if created.Binding.Principal != binding.Principal || created.Binding.CanonicalRepoID != binding.CanonicalRepoID ||
		created.Kind != artifacts.Kind(wantKind) || created.Provenance.CreatedBy != identity.Canonical {
		t.Fatalf("created artifact was not host-bound: %+v", created)
	}

	queryRaw, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "query-1",
		Payload: json.RawMessage(`{"kinds":["` + wantKind + `"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []artifacts.Artifact `json:"items"`
	}
	if err := json.Unmarshal(queryRaw, &result); err != nil || len(result.Items) != 1 || result.Items[0].ID != created.ID {
		t.Fatalf("query=%s parsed=%+v err=%v", queryRaw, result, err)
	}
}

func TestArtifactBrokerQueryResolvesExactImmutableReference(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)
	wantKind, err := identity.QualifiedKind("finding")
	if err != nil {
		t.Fatal(err)
	}
	create := func(requestID, summary string) artifacts.Artifact {
		t.Helper()
		raw, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
			BindingToken: binding.BindingToken, RequestID: requestID,
			Payload: json.RawMessage(`{"kind":"finding","scope":"session","data":{"summary":"` + summary + `"}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		var item artifacts.Artifact
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	selected := create("selected", "selected")
	for i := 0; i < 55; i++ {
		create(fmt.Sprintf("newer-%d", i), fmt.Sprintf("newer-%d", i))
	}
	payload, err := json.Marshal(map[string]any{
		"kinds": []string{wantKind},
		"refs":  []artifacts.ArtifactRef{{ID: selected.ID, Version: selected.Version}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "exact-query", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []artifacts.Artifact `json:"items"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Items) != 1 || result.Items[0].ID != selected.ID {
		t.Fatalf("exact query=%s parsed=%+v err=%v", raw, result, err)
	}
}

func TestArtifactBrokerRejectsGuestAuthorityAndCrossKindEdit(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)

	_, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "forged",
		Payload: json.RawMessage(`{"kind":"finding","scope":"global","principal":"mallory","data":{"summary":"x"}}`),
	})
	if err == nil {
		t.Fatal("guest-supplied principal was accepted")
	}

	createdRaw, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "create",
		Payload: json.RawMessage(`{"kind":"finding","scope":"global","data":{"summary":"x"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var created artifacts.Artifact
	_ = json.Unmarshal(createdRaw, &created)
	other := manifest
	other.ArtifactKinds = []plugins.ArtifactKindDef{{Name: "note", Schema: `{"type":"object"}`}}
	other.Capabilities = []string{"artifact:edit:note"}
	otherID, _ := plugins.RuntimeIdentityForLocal(other)
	otherBinding := bindArtifactFixture(t, svc, session, other, otherID)
	_, err = svc.artifactEdit(context.Background(), ArtifactCallParams{
		BindingToken: otherBinding.BindingToken, RequestID: "cross-kind",
		Payload: json.RawMessage(`{"kind":"note","id":"` + created.ID + `","expected_version":1,"data":{}}`),
	})
	if !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("cross-kind edit err=%v, want not found", err)
	}
}

func TestArtifactBrokerBindingDiesWithSession(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)
	if err := svc.TerminateSession(session.SessionID, session.controllerToken); err != nil {
		t.Fatal(err)
	}
	_, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "after-stop",
		Payload: json.RawMessage(`{"kinds":["local://reviewer#finding"]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown artifact binding") {
		t.Fatalf("terminated session binding err=%v", err)
	}
	svc.artifacts.mu.RLock()
	remaining := len(svc.artifacts.bindings)
	svc.artifacts.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("terminated session retained %d artifact binding(s)", remaining)
	}
}

func TestApplicationBindDispatchMintsAuthenticatedAnchor(t *testing.T) {
	svc, session, manifest, _ := artifactBrokerFixture(t)
	manifest.Lifecycle = &plugins.LifecycleDef{Points: []string{"post_turn"}}
	manifest.Capabilities = append(manifest.Capabilities, "lifecycle:observe:post_turn")
	identity, err := plugins.RuntimeIdentityForLocal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ApplicationBindParams{
		SessionID: session.SessionID, ControllerToken: session.controllerToken,
		Identity: identity, Manifest: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := svc.Dispatch(context.Background(), MethodApplicationBind, raw)
	if err != nil {
		t.Fatal(err)
	}
	var result ApplicationBindResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatal(err)
	}
	if result.BindingToken == "" || result.SessionID != session.SessionID || result.SessionGeneration != 1 || result.Principal == "" {
		t.Fatalf("application binding=%+v", result)
	}
}

func TestApplicationCallRechecksCapabilityAndProjectsSchedule(t *testing.T) {
	svc, session, manifest, _ := artifactBrokerFixture(t)
	manifest.Lifecycle = &plugins.LifecycleDef{}
	manifest.Capabilities = append(manifest.Capabilities,
		"session:journal:append", "session:projection:read", "session:schedule", "timer:schedule",
	)
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binding := bindApplicationFixture(t, svc, session, manifest, identity)

	call := func(operation, requestID, payload string) json.RawMessage {
		t.Helper()
		raw, err := svc.applicationCall(context.Background(), ApplicationCallParams{
			BindingToken: binding.BindingToken, RequestID: requestID,
			Operation: operation, Payload: json.RawMessage(payload),
		})
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		return raw
	}
	call("journal.append", "journal-1", `{"run_id":"run-1","kind":"worker.turn","summary":"turn committed"}`)
	holdRaw := call("hold.acquire", "hold-1", `{"id":"hold-1","run_id":"run-1","reason_code":"watchdog.review","ttl_ms":60000}`)
	var hold struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(holdRaw, &hold); err != nil || hold.ID != "hold-1" {
		t.Fatalf("hold=%s err=%v", holdRaw, err)
	}
	call("session.stop", "stop-1", `{"run_id":"run-1","reason_code":"watchdog.stop","hold_id":"hold-1"}`)

	schedule, err := svc.sessionSchedule(context.Background(), session.SessionID, session.controllerToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.ActiveHolds) != 1 || schedule.LatestStop == nil || schedule.LatestStop.RunID != "run-1" || schedule.AsOfSequence == 0 {
		t.Fatalf("schedule=%+v", schedule)
	}
	projectionRaw := call("projection.read", "read-1", `{"journal_limit":10,"control_limit":10}`)
	if !strings.Contains(string(projectionRaw), "turn committed") || !strings.Contains(string(projectionRaw), "watchdog.stop") {
		t.Fatalf("projection=%s", projectionRaw)
	}
}

func TestApplicationCallRejectsHostGateBypassAndGuestAuthority(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)
	_, err := svc.applicationCall(context.Background(), ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "bypass", Operation: "hold.acquire",
		Payload: json.RawMessage(`{"run_id":"run","reason_code":"review","ttl_ms":1000}`),
	})
	if err == nil || !strings.Contains(err.Error(), "artifact-only binding") {
		t.Fatalf("artifact binding reached lifecycle services: %v", err)
	}

	manifest.Lifecycle = &plugins.LifecycleDef{}
	manifest.Capabilities = append(manifest.Capabilities, "session:journal:append")
	identity, err = plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binding = bindApplicationFixture(t, svc, session, manifest, identity)
	_, err = svc.applicationCall(context.Background(), ApplicationCallParams{
		BindingToken: binding.BindingToken, RequestID: "forged", Operation: "journal.append",
		Payload: json.RawMessage(`{"run_id":"run","kind":"event","summary":"x","session_id":"other"}`),
	})
	if err == nil {
		t.Fatal("guest authority field was accepted")
	}
}
