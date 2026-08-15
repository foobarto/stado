package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

type artifactBridgeCall struct {
	operation string
	caller    ArtifactCaller
	payload   []byte
}

type recordingArtifactBridge struct {
	calls    []artifactBridgeCall
	lastCtx  context.Context
	response []byte
	err      error
	block    bool
	entered  chan struct{}
}

func (b *recordingArtifactBridge) invoke(ctx context.Context, operation string, caller ArtifactCaller, payload []byte) ([]byte, error) {
	b.lastCtx = ctx
	b.calls = append(b.calls, artifactBridgeCall{operation: operation, caller: caller, payload: append([]byte(nil), payload...)})
	if b.block {
		if b.entered != nil {
			close(b.entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return append([]byte(nil), b.response...), b.err
}

func (b *recordingArtifactBridge) Propose(ctx context.Context, caller ArtifactCaller, _ string, payload []byte) ([]byte, error) {
	return b.invoke(ctx, "propose", caller, payload)
}
func (b *recordingArtifactBridge) Query(ctx context.Context, caller ArtifactCaller, _ string, payload []byte) ([]byte, error) {
	return b.invoke(ctx, "query", caller, payload)
}
func (b *recordingArtifactBridge) Edit(ctx context.Context, caller ArtifactCaller, _ string, payload []byte) ([]byte, error) {
	return b.invoke(ctx, "edit", caller, payload)
}
func (b *recordingArtifactBridge) Observe(ctx context.Context, caller ArtifactCaller, _ string, payload []byte) ([]byte, error) {
	return b.invoke(ctx, "observe", caller, payload)
}

func artifactTestHarness(t *testing.T, caps ...string) *bridgeHarness {
	t.Helper()
	h := newBridgeHarness(t)
	h.host.Manifest.ArtifactKinds = []plugins.ArtifactKindDef{{
		Name: "contract", Schema: `{"type":"object"}`,
	}}
	return h.withCaps(caps...)
}

func TestArtifactBridgeCapabilityGateDeniesBeforeBridge(t *testing.T) {
	bridge := &recordingArtifactBridge{response: []byte(`{"ok":true}`)}
	h := artifactTestHarness(t).withArtifactBridge(bridge).install()
	requests := []struct {
		name string
		body string
	}{
		{"stado_artifact_propose", `{"kind":"contract","data":{}}`},
		{"stado_artifact_query", `{"kinds":["local://test#contract"]}`},
		{"stado_artifact_edit", `{"kind":"contract","id":"art_1","data":{}}`},
		{"stado_artifact_observe", `{"kind":"local://test#contract","event":"opened"}`},
	}
	for _, test := range requests {
		h.memWrite(0, []byte(test.body))
		if got := h.callImport(context.Background(), test.name, 0, uint64(len(test.body)), 1024, 256); got >= 0 {
			t.Errorf("%s without capability returned %d, want negative", test.name, got)
		}
	}
	if len(bridge.calls) != 0 {
		t.Fatalf("denied requests reached bridge: %+v", bridge.calls)
	}
}

func TestArtifactBridgeNilFailsClosed(t *testing.T) {
	h := artifactTestHarness(t, "artifact:propose:contract").install()
	body := []byte(`{"kind":"contract","data":{}}`)
	h.memWrite(0, body)
	if got := h.callImport(context.Background(), "stado_artifact_propose", 0, uint64(len(body)), 1024, 256); got >= 0 {
		t.Fatalf("nil bridge returned %d, want negative", got)
	}
}

func TestArtifactBridgeInjectsIdentityAndScope(t *testing.T) {
	bridge := &recordingArtifactBridge{response: []byte(`{"id":"art_1","version":1}`)}
	h := artifactTestHarness(t, "artifact:propose:contract").withArtifactBridge(bridge)
	h.host.ArtifactCaller = ArtifactCallerContext{
		Principal: "operator", CanonicalRepoID: "repo:1", SessionID: "ses_1",
		SessionGeneration: 4, AncestorSessionIDs: []string{"ses_root"},
	}
	h.install()
	body := []byte(`{"kind":"contract","data":{"objective":"review"}}`)
	h.memWrite(0, body)
	got := h.callImport(context.Background(), "stado_artifact_propose", 0, uint64(len(body)), 1024, 256)
	if got <= 0 {
		t.Fatalf("propose returned %d", got)
	}
	if len(bridge.calls) != 1 || bridge.calls[0].operation != "propose" || !bytes.Equal(bridge.calls[0].payload, body) {
		t.Fatalf("bridge calls = %+v", bridge.calls)
	}
	call := bridge.calls[0]
	if call.caller.Identity.Namespace != h.host.Identity.Namespace || call.caller.Principal != "operator" ||
		call.caller.SessionID != "ses_1" || call.caller.SessionGeneration != 4 {
		t.Fatalf("caller not host-bound: %+v", call.caller)
	}
	if gotBody := h.memRead(1024, uint32(got)); string(gotBody) != string(bridge.response) {
		t.Fatalf("response = %q", gotBody)
	}
}

func TestArtifactBridgeRejectsUndeclaredLocalKind(t *testing.T) {
	bridge := &recordingArtifactBridge{response: []byte(`{}`)}
	h := artifactTestHarness(t, "artifact:propose:other").withArtifactBridge(bridge).install()
	body := []byte(`{"kind":"other","data":{}}`)
	h.memWrite(0, body)
	if got := h.callImport(context.Background(), "stado_artifact_propose", 0, uint64(len(body)), 1024, 256); got >= 0 {
		t.Fatalf("undeclared kind returned %d", got)
	}
	if len(bridge.calls) != 0 {
		t.Fatal("undeclared kind reached bridge")
	}
}

func TestArtifactBridgeQualifiedReadPatterns(t *testing.T) {
	bridge := &recordingArtifactBridge{response: []byte(`{"items":[]}`)}
	h := artifactTestHarness(t, "artifact:read:github.com/acme/reviewer#*").withArtifactBridge(bridge).install()

	allowed := []byte(`{"kinds":["github.com/acme/reviewer#contract"]}`)
	h.memWrite(0, allowed)
	if got := h.callImport(context.Background(), "stado_artifact_query", 0, uint64(len(allowed)), 1024, 256); got <= 0 {
		t.Fatalf("allowed query returned %d", got)
	}

	denied := []byte(`{"kinds":["github.com/other/reviewer#contract"]}`)
	h.memWrite(0, denied)
	if got := h.callImport(context.Background(), "stado_artifact_query", 0, uint64(len(denied)), 1024, 256); got >= 0 {
		t.Fatalf("cross-plugin query returned %d", got)
	}
	if len(bridge.calls) != 1 {
		t.Fatalf("denied query reached bridge: %+v", bridge.calls)
	}
}

func TestArtifactBridgeResolvesSelfKindFromAuthenticatedIdentity(t *testing.T) {
	bridge := &recordingArtifactBridge{response: []byte(`{"items":[]}`)}
	h := artifactTestHarness(t, "artifact:read:self#contract").withArtifactBridge(bridge).install()

	body := []byte(`{"kinds":["self#contract"]}`)
	h.memWrite(0, body)
	if got := h.callImport(context.Background(), "stado_artifact_query", 0, uint64(len(body)), 1024, 256); got <= 0 {
		t.Fatalf("self query returned %d", got)
	}
	if len(bridge.calls) != 1 {
		t.Fatalf("bridge calls = %+v", bridge.calls)
	}
	var request struct {
		Kinds []string `json:"kinds"`
	}
	if err := json.Unmarshal(bridge.calls[0].payload, &request); err != nil {
		t.Fatal(err)
	}
	want, err := h.host.Identity.QualifiedKind("contract")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Kinds) != 1 || request.Kinds[0] != want {
		t.Fatalf("resolved kinds = %v, want %q", request.Kinds, want)
	}

	singular := []byte(`{"kind":"self#contract"}`)
	h.memWrite(0, singular)
	if got := h.callImport(context.Background(), "stado_artifact_query", 0, uint64(len(singular)), 1024, 256); got <= 0 {
		t.Fatalf("singular self query returned %d", got)
	}
	if err := json.Unmarshal(bridge.calls[1].payload, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Kinds) != 1 || request.Kinds[0] != want {
		t.Fatalf("singular query was not canonicalized: %+v", request)
	}

	denied := []byte(`{"kinds":["self#other"]}`)
	h.memWrite(0, denied)
	if got := h.callImport(context.Background(), "stado_artifact_query", 0, uint64(len(denied)), 1024, 256); got >= 0 {
		t.Fatalf("undeclared self kind returned %d", got)
	}
	if len(bridge.calls) != 2 {
		t.Fatal("denied self kind reached bridge")
	}

	ambiguous := []byte(`{"kind":"self#contract","kinds":["self#contract"]}`)
	h.memWrite(0, ambiguous)
	if got := h.callImport(context.Background(), "stado_artifact_query", 0, uint64(len(ambiguous)), 1024, 256); got >= 0 {
		t.Fatalf("ambiguous self kind request returned %d", got)
	}
}

func TestArtifactBridgeEditAndObserveAreIndependentCapabilities(t *testing.T) {
	bridge := &recordingArtifactBridge{response: []byte(`{"ok":true}`)}
	h := artifactTestHarness(t,
		"artifact:edit:contract",
		"artifact:observe:local://test#contract",
	).withArtifactBridge(bridge).install()
	for _, test := range []struct{ name, body string }{
		{"stado_artifact_edit", `{"kind":"contract","id":"art_1","expected_version":1,"data":{}}`},
		{"stado_artifact_observe", `{"kind":"local://test#contract","event":"opened"}`},
	} {
		h.memWrite(0, []byte(test.body))
		if got := h.callImport(context.Background(), test.name, 0, uint64(len(test.body)), 1024, 256); got <= 0 {
			t.Fatalf("%s returned %d", test.name, got)
		}
	}
	if len(bridge.calls) != 2 || bridge.calls[0].operation != "edit" || bridge.calls[1].operation != "observe" {
		t.Fatalf("calls = %+v", bridge.calls)
	}
}

func TestArtifactBridgePropagatesCancellation(t *testing.T) {
	bridge := &recordingArtifactBridge{block: true, entered: make(chan struct{})}
	h := artifactTestHarness(t, "artifact:propose:contract").withArtifactBridge(bridge).install()
	body := []byte(`{"kind":"contract","data":{}}`)
	h.memWrite(0, body)
	ctx, cancel := context.WithCancel(context.Background())
	fn := h.thunkMod.ExportedFunction("thunk_stado_artifact_propose")
	done := make(chan error, 1)
	go func() {
		_, err := fn.Call(ctx, 0, uint64(len(body)), 1024, 256)
		done <- err
	}()
	<-bridge.entered
	cancel()
	// Runtime.New enables wazero's CloseOnContextDone. The runtime may close
	// the guest module before the host closure can encode its negative result,
	// so the wire result is deliberately not part of the cancellation contract.
	// The contract is that the exact call context reaches and unblocks the
	// broker bridge, matching the session/UI/fleet bridge tests.
	<-done
	var bridgeErr error
	if bridge.lastCtx != nil {
		bridgeErr = bridge.lastCtx.Err()
	}
	if len(bridge.calls) != 1 || !errors.Is(bridgeErr, context.Canceled) {
		t.Fatalf("cancellation did not reach bridge: calls=%d bridge_err=%v", len(bridge.calls), bridgeErr)
	}
}

func TestRemovedMemoryImportsAreNotExported(t *testing.T) {
	h := newBridgeHarness(t).install()
	hostModule := h.rt.rt.Module(NamespaceStado)
	exports := hostModule.ExportedFunctionDefinitions()
	for _, name := range []string{"stado_memory_propose", "stado_memory_query", "stado_memory_update"} {
		if _, ok := exports[name]; ok {
			t.Errorf("removed legacy import %s is still exported", name)
		}
	}
}
