package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

type fixedEvidenceSource struct{}

func (fixedEvidenceSource) AuthorizedSessions(context.Context, EvidenceSessionScope) ([]string, error) {
	return []string{"logical-parent"}, nil
}
func (fixedEvidenceSource) Catalog(context.Context, EvidenceSessionScope, int) ([]EvidenceItem, error) {
	return nil, nil
}
func (fixedEvidenceSource) Search(context.Context, EvidenceSessionScope, string, int) ([]EvidenceItem, error) {
	return nil, nil
}
func (fixedEvidenceSource) Open(_ context.Context, _ EvidenceSessionScope, ref EvidenceRef, _ int) (EvidenceOpened, error) {
	return EvidenceOpened{Ref: ref, Body: "bounded immutable evidence " + ref.ID}, nil
}

func evidenceFixture(t *testing.T) (*Service, artifactBinding, artifactBinding, *wal.Store) {
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
	if err := svc.ConfigureSessionEvidenceSource(fixedEvidenceSource{}); err != nil {
		t.Fatal(err)
	}
	svc.sessions["parent"] = &sessionState{handle: SessionHandle{SessionID: "parent", Purpose: PurposeMainChat, CWD: "/repo"}, controllerVersion: 1, generation: 1, principal: "p", repoID: "r", scope: sessionScopeState{durable: true, subject: "logical-parent"}}
	svc.sessions["child"] = &sessionState{handle: SessionHandle{SessionID: "child", Purpose: PurposeSubagent}, controllerVersion: 1, generation: 1, principal: "p", repoID: "r", parentID: "parent", role: "explorer", mode: "read_only"}
	identity := plugins.RuntimeIdentity{Canonical: "github.com/foobarto/stado-plugins/research@v1.0.0", Namespace: "github.com/foobarto/stado-plugins/research"}
	child := artifactBinding{token: "child-binding", sessionID: "child", generation: 1, controllerVersion: 1, principal: "p", repoID: "r", identity: identity,
		capabilities: capabilitySet([]string{"evidence:open:session"})}
	parent := artifactBinding{token: "parent-binding", sessionID: "parent", generation: 1, controllerVersion: 1, principal: "p", repoID: "r", identity: identity,
		capabilities: capabilitySet([]string{"evidence:validate"})}
	svc.artifacts.bindings[child.token] = child
	svc.artifacts.bindings[parent.token] = parent
	return svc, child, parent, store
}

func evidenceOpenPayload(id string) json.RawMessage {
	body := "bounded immutable evidence " + id
	ref := EvidenceRef{Corpus: "session", Kind: "conversation-record", ID: id, Locator: "conversation.jsonl:bytes:0-1", Digest: evidenceDigest([]byte(body))}
	raw, _ := json.Marshal(map[string]any{"corpus": "session", "ref": ref})
	return raw
}

func TestEvidenceBudgetSerializesDistinctConcurrentOpens(t *testing.T) {
	svc, child, _, store := evidenceFixture(t)
	defer store.Close()
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := svc.evidenceRead(context.Background(), child, "open", evidenceOpenPayload(fmt.Sprintf("item-%02d", i))); err == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := ok.Load(); got != maxEvidenceOpens {
		t.Fatalf("successful opens=%d, want exact ceiling %d", got, maxEvidenceOpens)
	}
	usage := foldEvidenceUsage(store.Records(), evidenceScopeKey(child))
	if usage.Opens != maxEvidenceOpens || usage.Calls != maxEvidenceOpens {
		t.Fatalf("durable usage=%+v, want %d opens/calls", usage, maxEvidenceOpens)
	}
}

func TestEvidenceConcurrentIdempotentOpenFoldsOnce(t *testing.T) {
	svc, child, _, store := evidenceFixture(t)
	defer store.Close()
	payload := evidenceOpenPayload("same")
	var failed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.evidenceRead(context.Background(), child, "open", payload); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("idempotent open failures=%d", failed.Load())
	}
	usage := foldEvidenceUsage(store.Records(), evidenceScopeKey(child))
	if usage.Opens != 1 || usage.Calls != 1 {
		t.Fatalf("idempotent usage=%+v, want one logical call", usage)
	}
}

func TestEvidenceValidationRejectsFabricatedAndForeignCitations(t *testing.T) {
	svc, child, parent, store := evidenceFixture(t)
	defer store.Close()
	payload := evidenceOpenPayload("opened")
	openedRaw, err := svc.evidenceRead(context.Background(), child, "open", payload)
	if err != nil {
		t.Fatal(err)
	}
	var opened EvidenceOpened
	if err := json.Unmarshal(openedRaw, &opened); err != nil {
		t.Fatal(err)
	}
	result := func(ref EvidenceRef, excerpt, childID string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"child_session": childID, "result": map[string]any{
			"answer": "answer", "confidence": "medium", "claims": []any{map[string]any{
				"text": "claim", "citations": []any{map[string]any{"ref": ref, "excerpt": excerpt, "entailment_verified": true}},
			}},
		}})
		return raw
	}
	valid, err := svc.validateEvidenceResult(parent, result(opened.Ref, "immutable evidence", "child"))
	if err != nil {
		t.Fatalf("valid citation: %v", err)
	}
	if string(valid) == "" || containsJSONTrue(valid, "entailment_verified") {
		t.Fatalf("validated result retained semantic-entailment claim: %s", valid)
	}
	fabricated := opened.Ref
	fabricated.Digest = evidenceDigest([]byte("fabricated"))
	if _, err := svc.validateEvidenceResult(parent, result(fabricated, "immutable evidence", "child")); err == nil {
		t.Fatal("fabricated citation passed validation")
	}
	if _, err := svc.validateEvidenceResult(parent, result(opened.Ref, "not present", "child")); err == nil {
		t.Fatal("fabricated excerpt passed validation")
	}
	svc.sessions["foreign"] = &sessionState{handle: SessionHandle{SessionID: "foreign", Purpose: PurposeSubagent}, parentID: "other", role: "explorer", mode: "read_only", generation: 1}
	if _, err := svc.validateEvidenceResult(parent, result(opened.Ref, "immutable evidence", "foreign")); err == nil {
		t.Fatal("foreign child passed validation")
	}
	svc.sessions["child"].mode = "workspace_write"
	if _, err := svc.validateEvidenceResult(parent, result(opened.Ref, "immutable evidence", "child")); err == nil {
		t.Fatal("write-capable direct child passed validation")
	}
	svc.sessions["child"].mode = "read_only"
	svc.sessions["child"].handle.Purpose = PurposeToolRun
	if _, err := svc.validateEvidenceResult(parent, result(opened.Ref, "immutable evidence", "child")); err == nil {
		t.Fatal("non-subagent direct session passed validation")
	}
}

func TestArtifactEvidenceReceiptIDsAreResolvedForExactLifecycleBinding(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(DefaultPolicy(), nil)
	manifest := plugins.Manifest{
		Name: "memory", Version: "v1.0.0", Lifecycle: &plugins.LifecycleDef{},
		ArtifactKinds: []plugins.ArtifactKindDef{{Name: "lesson", Schema: `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`}},
		Capabilities:  []string{"artifact:propose:lesson", "evidence:open:session"},
	}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	verifier := ArtifactPluginVerifierFunc(func(_ context.Context, requested plugins.RuntimeIdentity, _ plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
		if requested != identity {
			return plugins.RuntimeIdentity{}, plugins.Manifest{}, plugins.ErrRuntimeIdentityNotFound
		}
		return identity, manifest, nil
	})
	if err := service.ConfigureArtifactStore(store, verifier); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureSessionEvidenceSource(fixedEvidenceSource{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	newSession := func(subject string) SessionHandle {
		handle, decision, err := service.CreateSession(CapabilityRequest{Purpose: PurposeMainChat, Profile: ProfileDefault, CWD: t.TempDir()})
		if err != nil || !decision.Admit {
			t.Fatalf("session=%+v decision=%+v err=%v", handle, decision, err)
		}
		service.sessionsMu.Lock()
		service.sessions[handle.SessionID].scope = sessionScopeState{durable: true, subject: subject}
		service.sessionsMu.Unlock()
		return handle
	}
	bind := func(session SessionHandle) ArtifactBindResult {
		result, err := service.bindApplication(context.Background(), ApplicationBindParams{
			SessionID: session.SessionID, ControllerToken: session.controllerToken, Identity: identity, Manifest: manifest,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	firstSession := newSession("logical-first")
	first := bind(firstSession)
	openedRaw, err := service.evidenceCall(context.Background(), EvidenceCallParams{
		BindingToken: first.BindingToken, Operation: "open", Payload: evidenceOpenPayload("turn-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var opened EvidenceOpened
	if err := json.Unmarshal(openedRaw, &opened); err != nil || !validSHA256Digest(opened.ReceiptID) {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	propose := func(token, requestID string, receiptIDs []string, free []string) (json.RawMessage, error) {
		payload, _ := json.Marshal(map[string]any{
			"kind": "lesson", "scope": "session", "data": map[string]string{"summary": "candidate"},
			"evidence_receipt_ids": receiptIDs, "evidence_refs": free,
		})
		return service.artifactPropose(context.Background(), ArtifactCallParams{BindingToken: token, RequestID: requestID, Payload: payload})
	}
	createdRaw, err := propose(first.BindingToken, "candidate-1", []string{opened.ReceiptID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var created artifacts.Artifact
	if err := json.Unmarshal(createdRaw, &created); err != nil || len(created.EvidenceRefs) != 1 || created.EvidenceRefs[0] != "broker:evidence-receipt:"+opened.ReceiptID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err := propose(first.BindingToken, "forged", []string{"sha256:" + strings.Repeat("f", 64)}, nil); err == nil {
		t.Fatal("fabricated evidence receipt was accepted")
	}
	if _, err := propose(first.BindingToken, "reserved-free-form", nil, []string{"broker:evidence-receipt:" + opened.ReceiptID}); err == nil {
		t.Fatal("free-form evidence ref spoofed the broker-derived namespace")
	}

	// A lifecycle rebind replaces the bearer but preserves the exact admitted
	// session/generation/plugin tuple, so the durable receipt remains usable.
	rebound := bind(firstSession)
	if _, err := propose(first.BindingToken, "stale-old-token", []string{opened.ReceiptID}, nil); err == nil {
		t.Fatal("superseded lifecycle bearer remained usable")
	}
	if _, err := propose(rebound.BindingToken, "candidate-2", []string{opened.ReceiptID}, nil); err != nil {
		t.Fatalf("exact lifecycle rebind lost its evidence receipt: %v", err)
	}

	other := bind(newSession("logical-other"))
	if _, err := propose(other.BindingToken, "cross-session", []string{opened.ReceiptID}, nil); err == nil {
		t.Fatal("evidence receipt crossed sessions")
	}
	service.sessionsMu.Lock()
	service.sessions[firstSession.SessionID].generation++
	service.sessionsMu.Unlock()
	if _, err := propose(rebound.BindingToken, "stale-generation", []string{opened.ReceiptID}, nil); err == nil || !strings.Contains(err.Error(), "stale artifact binding") {
		t.Fatalf("evidence receipt bypassed generation fence: %v", err)
	}
}

func containsJSONTrue(raw []byte, field string) bool {
	return len(raw) != 0 && json.Valid(raw) && bytes.Contains(raw, []byte(`"`+field+`":true`))
}
