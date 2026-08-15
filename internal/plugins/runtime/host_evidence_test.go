package runtime

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

type evidenceBridgeCall struct {
	operation string
	payload   []byte
}

type recordingEvidenceBridge struct {
	calls    []evidenceBridgeCall
	response []byte
	err      error
}

func (b *recordingEvidenceBridge) CallEvidence(_ context.Context, operation string, payload []byte) ([]byte, error) {
	b.calls = append(b.calls, evidenceBridgeCall{operation: operation, payload: append([]byte(nil), payload...)})
	return append([]byte(nil), b.response...), b.err
}

func evidenceHarness(t *testing.T, bridge EvidenceBridge, caps ...string) *bridgeHarness {
	t.Helper()
	h := newBridgeHarness(t).withCaps(caps...).withEvidenceBridge(bridge)
	h.withApplicationScope("ses_test", 4)
	return h.install()
}

func TestEvidenceBridgeCapabilityGateDeniesBeforeBridge(t *testing.T) {
	bridge := &recordingEvidenceBridge{response: []byte(`{"items":[]}`)}
	h := evidenceHarness(t, bridge)
	request := []byte(`{"corpus":"artifact"}`)
	h.memWrite(0, request)
	if got := h.callImport(context.Background(), "stado_evidence_catalog", 0, uint64(len(request)), 1024, 256); got >= 0 {
		t.Fatalf("denied catalog returned %d", got)
	}
	if len(bridge.calls) != 0 {
		t.Fatalf("denied call reached bridge: %+v", bridge.calls)
	}
}

func TestEvidenceBridgeRequiresIdentityAndBridge(t *testing.T) {
	h := newBridgeHarness(t).withCaps("evidence:catalog:artifact").install()
	request := []byte(`{"corpus":"artifact"}`)
	h.memWrite(0, request)
	if got := h.callImport(context.Background(), "stado_evidence_catalog", 0, uint64(len(request)), 1024, 256); got >= 0 {
		t.Fatalf("missing identity and bridge returned %d", got)
	}

	bridge := &recordingEvidenceBridge{response: []byte(`{}`)}
	h = newBridgeHarness(t).withCaps("evidence:catalog:artifact").withEvidenceBridge(bridge).install()
	h.host.Identity = plugins.RuntimeIdentity{}
	h.memWrite(0, request)
	if got := h.callImport(context.Background(), "stado_evidence_catalog", 0, uint64(len(request)), 1024, 256); got >= 0 {
		t.Fatalf("missing identity returned %d", got)
	}
	if len(bridge.calls) != 0 {
		t.Fatal("unauthenticated call reached bridge")
	}
}

func TestEvidenceBridgeForwardsOnlyExactCorpusCapabilities(t *testing.T) {
	bridge := &recordingEvidenceBridge{response: []byte(`{"items":[]}`)}
	h := evidenceHarness(t, bridge,
		"evidence:catalog:artifact",
		"evidence:search:session",
		"evidence:open:artifact",
		"evidence:validate",
	)
	tests := []struct {
		name string
		body string
	}{
		{"stado_evidence_catalog", `{"corpus":"artifact"}`},
		{"stado_evidence_search", `{"corpus":"session","query":"needle"}`},
		{"stado_evidence_open", `{"corpus":"artifact","ref":{"id":"art_1"}}`},
		{"stado_evidence_validate", `{"child_session":"ses_child","result":{}}`},
	}
	for _, test := range tests {
		body := []byte(test.body)
		h.memWrite(0, body)
		got := h.callImport(context.Background(), test.name, 0, uint64(len(body)), 1024, 256)
		if got <= 0 {
			t.Fatalf("%s returned %d", test.name, got)
		}
	}
	if len(bridge.calls) != len(tests) {
		t.Fatalf("calls = %+v", bridge.calls)
	}
	for i, test := range tests {
		if bridge.calls[i].operation != test.name[len("stado_evidence_"):] || !bytes.Equal(bridge.calls[i].payload, []byte(test.body)) {
			t.Fatalf("call %d = %+v", i, bridge.calls[i])
		}
	}

	denied := []byte(`{"corpus":"session"}`)
	h.memWrite(0, denied)
	if got := h.callImport(context.Background(), "stado_evidence_open", 0, uint64(len(denied)), 1024, 256); got >= 0 {
		t.Fatalf("wrong-corpus open returned %d", got)
	}
	if len(bridge.calls) != len(tests) {
		t.Fatal("wrong-corpus call reached bridge")
	}
}

func TestEvidenceBridgeHidesBrokerErrors(t *testing.T) {
	bridge := &recordingEvidenceBridge{err: errors.New("secret broker detail")}
	h := evidenceHarness(t, bridge, "evidence:catalog:artifact")
	request := []byte(`{"corpus":"artifact"}`)
	h.memWrite(0, request)
	got := h.callImport(context.Background(), "stado_evidence_catalog", 0, uint64(len(request)), 1024, 256)
	if got >= 0 {
		t.Fatalf("broker error returned %d", got)
	}
}
