package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
			"artifact:read:self#finding", "artifact:observe:self#finding",
		},
		Tools: []plugins.ToolDef{{Name: "reviewer__run"}},
	}
	manifest.Tools[0].Capabilities = plugins.CapabilitySubset(manifest.Capabilities...)
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return svc, handle, manifest, identity
}

func TestArtifactBrokerResolvesSelfCapabilityAtAdmission(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)
	wantKind, err := identity.QualifiedKind("finding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "self-query",
		Payload: json.RawMessage(`{"kinds":["` + wantKind + `"]}`),
	}); err != nil {
		t.Fatalf("resolved self capability rejected: %v", err)
	}
	if _, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "literal-self-query",
		Payload: json.RawMessage(`{"kinds":["self#finding"]}`),
	}); err == nil {
		t.Fatal("broker accepted unresolved guest self namespace")
	}
}

func TestOrdinaryBrokerBindingsUseExactVerifiedToolCapabilitySubset(t *testing.T) {
	manifest := plugins.Manifest{
		Name: "mixed", Version: "v1.0.0",
		ArtifactKinds: []plugins.ArtifactKindDef{{
			Name: "finding", Schema: `{"type":"object","additionalProperties":false}`,
		}},
		Capabilities: []string{"artifact:propose:finding", "evidence:validate", "session:schedule"},
		Tools: []plugins.ToolDef{
			{Name: "mixed__search", Capabilities: plugins.CapabilitySubset()},
			{Name: "mixed__load", Capabilities: plugins.CapabilitySubset("artifact:propose:finding", "evidence:validate", "session:schedule")},
		},
	}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(DefaultPolicy(), nil)
	verifier := ArtifactPluginVerifierFunc(func(_ context.Context, requested plugins.RuntimeIdentity, _ plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		if requested != identity {
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, plugins.ErrRuntimeIdentityNotFound
		}
		return identity, manifest, nil
	})
	if err := svc.ConfigureArtifactStore(store, verifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	session, decision, err := svc.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir()})
	if err != nil || !decision.Admit {
		t.Fatalf("CreateSession decision=%+v err=%v", decision, err)
	}
	bind := func(method, toolName string, requested plugins.Manifest) (ArtifactBindResult, error) {
		params := ArtifactBindParams{SessionID: session.SessionID, ControllerToken: session.controllerToken, Identity: identity, Manifest: requested, ToolName: toolName}
		if method == MethodEvidenceBind {
			return svc.bindEvidence(context.Background(), EvidenceBindParams(params))
		}
		return svc.bindArtifacts(context.Background(), params)
	}

	searchArtifact, err := bind(MethodArtifactBind, "mixed__search", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: searchArtifact.BindingToken, RequestID: "search-forged-propose",
		Payload: json.RawMessage(`{"kind":"finding","scope":"session","data":{}}`),
	}); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("zero-authority search token reached artifact proposal: %v", err)
	}
	searchEvidence, err := bind(MethodEvidenceBind, "mixed__search", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.evidenceCall(context.Background(), EvidenceCallParams{BindingToken: searchEvidence.BindingToken, Operation: "validate", Payload: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "evidence:validate capability required") {
		t.Fatalf("zero-authority search token reached evidence validation: %v", err)
	}

	loadArtifact, err := bind(MethodArtifactBind, "mixed__load", manifest)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: loadArtifact.BindingToken, RequestID: "load-propose",
		Payload: json.RawMessage(`{"kind":"finding","scope":"session","data":{}}`),
	})
	if err != nil || len(created) == 0 {
		t.Fatalf("load tool artifact authority unavailable: result=%s err=%v", created, err)
	}
	loadEvidence, err := bind(MethodEvidenceBind, "mixed__load", manifest)
	if err != nil {
		t.Fatal(err)
	}
	svc.artifacts.mu.RLock()
	searchState := svc.artifacts.bindings[searchEvidence.BindingToken]
	loadState := svc.artifacts.bindings[loadEvidence.BindingToken]
	svc.artifacts.mu.RUnlock()
	if searchState.hasCapability("session:schedule") || searchState.hasCapability("evidence:validate") {
		t.Fatalf("search binding widened to sibling authority: %v", searchState.capabilities)
	}
	if !loadState.hasCapability("session:schedule") || !loadState.hasCapability("evidence:validate") {
		t.Fatalf("load binding lost selected authority: %v", loadState.capabilities)
	}
	if _, err := bind(MethodArtifactBind, "mixed__missing", manifest); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("unknown signed tool selector accepted: %v", err)
	}
	requestedMismatch := manifest
	requestedMismatch.Tools = append([]plugins.ToolDef(nil), manifest.Tools...)
	requestedMismatch.Tools[0].Name = "mixed__unsigned"
	if _, err := bind(MethodArtifactBind, "mixed__unsigned", requestedMismatch); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("request-only tool selector survived broker manifest reload: %v", err)
	}
}

func bindArtifactFixture(t *testing.T, svc *Service, handle SessionHandle, manifest plugins.Manifest, identity plugins.RuntimeIdentity) ArtifactBindResult {
	t.Helper()
	result, err := svc.bindArtifacts(context.Background(), ArtifactBindParams{
		SessionID: handle.SessionID, ControllerToken: handle.controllerToken,
		Identity: identity, Manifest: manifest, ToolName: manifest.Tools[0].Name,
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

func TestArtifactBrokerGlobalMutationIdempotencyConvergesAcrossSessions(t *testing.T) {
	svc, firstSession, manifest, identity := artifactBrokerFixture(t)
	firstBinding := bindArtifactFixture(t, svc, firstSession, manifest, identity)
	secondSession, decision, err := svc.CreateSession(CapabilityRequest{
		Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir(),
	})
	if err != nil || !decision.Admit {
		t.Fatalf("second session decision=%+v err=%v", decision, err)
	}
	secondBinding := bindArtifactFixture(t, svc, secondSession, manifest, identity)
	payload := json.RawMessage(`{"kind":"finding","scope":"global","data":{"summary":"legacy item"}}`)

	const callers = 24
	results := make(chan artifacts.Artifact, callers)
	errorsOut := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		binding := firstBinding
		if i%2 != 0 {
			binding = secondBinding
		}
		go func() {
			defer wg.Done()
			raw, callErr := svc.artifactPropose(context.Background(), ArtifactCallParams{
				BindingToken: binding.BindingToken, RequestID: "legacy:digest:item-1", Payload: payload,
			})
			if callErr != nil {
				errorsOut <- callErr
				return
			}
			var item artifacts.Artifact
			if decodeErr := json.Unmarshal(raw, &item); decodeErr != nil {
				errorsOut <- decodeErr
				return
			}
			results <- item
		}()
	}
	wg.Wait()
	close(results)
	close(errorsOut)
	for callErr := range errorsOut {
		t.Fatalf("concurrent idempotent proposal: %v", callErr)
	}
	wantID := ""
	for item := range results {
		if wantID == "" {
			wantID = item.ID
		}
		if item.ID != wantID || item.Version != 1 {
			t.Fatalf("proposal result=%+v want id=%s version=1", item, wantID)
		}
	}
	if wantID == "" {
		t.Fatal("no proposal result")
	}
	qualified, _ := identity.QualifiedKind("finding")
	queryRaw, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
		BindingToken: firstBinding.BindingToken, RequestID: "verify-global",
		Payload: json.RawMessage(`{"kinds":["` + qualified + `"],"max_items":50}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var query struct {
		Items []artifacts.Artifact `json:"items"`
	}
	if err := json.Unmarshal(queryRaw, &query); err != nil || len(query.Items) != 1 || query.Items[0].ID != wantID {
		t.Fatalf("global idempotency query=%s parsed=%+v err=%v", queryRaw, query, err)
	}
	if _, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: secondBinding.BindingToken, RequestID: "legacy:digest:item-1",
		Payload: json.RawMessage(`{"kind":"finding","scope":"global","data":{"summary":"changed"}}`),
	}); !errors.Is(err, wal.ErrConflict) {
		t.Fatalf("changed retry err=%v, want WAL conflict", err)
	}

	firstSessionItemRaw, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: firstBinding.BindingToken, RequestID: "same-logical-session-key",
		Payload: json.RawMessage(`{"kind":"finding","scope":"session","data":{"summary":"session item"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondSessionItemRaw, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: secondBinding.BindingToken, RequestID: "same-logical-session-key",
		Payload: json.RawMessage(`{"kind":"finding","scope":"session","data":{"summary":"session item"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var firstItem, secondItem artifacts.Artifact
	_ = json.Unmarshal(firstSessionItemRaw, &firstItem)
	_ = json.Unmarshal(secondSessionItemRaw, &secondItem)
	if firstItem.ID == secondItem.ID || firstItem.Binding.AnchorSessionID == secondItem.Binding.AnchorSessionID {
		t.Fatalf("session-scoped idempotency crossed bindings: first=%+v second=%+v", firstItem, secondItem)
	}
}

func TestArtifactBrokerDigestFencedPaginationRejectsConcurrentDrift(t *testing.T) {
	svc, session, manifest, identity := artifactBrokerFixture(t)
	binding := bindArtifactFixture(t, svc, session, manifest, identity)
	qualified, _ := identity.QualifiedKind("finding")
	for i := 0; i < 61; i++ {
		if _, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
			BindingToken: binding.BindingToken, RequestID: fmt.Sprintf("page-create-%02d", i),
			Payload: json.RawMessage(fmt.Sprintf(`{"kind":"finding","scope":"global","data":{"summary":"item-%02d"}}`, i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	type pageResult struct {
		Items      []artifacts.Artifact `json:"items"`
		PageDigest string               `json:"page_digest"`
		NextOffset int                  `json:"next_offset"`
		Complete   bool                 `json:"complete"`
	}
	query := func(requestID string, offset int, digest string) (pageResult, error) {
		payload, err := json.Marshal(map[string]any{
			"kinds": []string{qualified}, "max_items": 17, "page_offset": offset, "page_digest": digest,
		})
		if err != nil {
			return pageResult{}, err
		}
		raw, err := svc.artifactQuery(context.Background(), ArtifactCallParams{
			BindingToken: binding.BindingToken, RequestID: requestID, Payload: payload,
		})
		if err != nil {
			return pageResult{}, err
		}
		var page pageResult
		err = json.Unmarshal(raw, &page)
		return page, err
	}
	first, err := query("page-1", 0, "")
	if err != nil || len(first.Items) != 17 || first.NextOffset != 17 || first.Complete || !validSHA256Digest(first.PageDigest) {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := query("page-2", first.NextOffset, first.PageDigest)
	if err != nil || len(second.Items) != 17 || second.PageDigest != first.PageDigest {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	if _, err := query("missing-fence", first.NextOffset, ""); err == nil || !strings.Contains(err.Error(), "page_digest is required") {
		t.Fatalf("offset without digest err=%v", err)
	}
	if _, err := svc.artifactPropose(context.Background(), ArtifactCallParams{
		BindingToken: binding.BindingToken, RequestID: "page-drift",
		Payload: json.RawMessage(`{"kind":"finding","scope":"global","data":{"summary":"concurrent"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := query("stale-page-2", first.NextOffset, first.PageDigest); err == nil || !strings.Contains(err.Error(), "projection changed") {
		t.Fatalf("stale page fence err=%v", err)
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
	other.Tools[0].Capabilities = plugins.CapabilitySubset(other.Capabilities...)
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
	manifest.Tools[0].Capabilities = nil
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
	manifest.Tools[0].Capabilities = nil
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
	manifest.Tools[0].Capabilities = nil
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
