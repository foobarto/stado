package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type applicationBridgeCall struct {
	operation string
	requestID string
	payload   []byte
}

type recordingApplicationBridge struct {
	calls    []applicationBridgeCall
	response []byte
	err      error
}

func (b *recordingApplicationBridge) CallApplication(_ context.Context, operation, requestID string, payload []byte) ([]byte, error) {
	b.calls = append(b.calls, applicationBridgeCall{operation: operation, requestID: requestID, payload: append([]byte(nil), payload...)})
	return append([]byte(nil), b.response...), b.err
}

func TestApplicationImportsUseFixedOperationAndExactCapability(t *testing.T) {
	for _, definition := range applicationImports {
		t.Run(definition.operation, func(t *testing.T) {
			bridge := &recordingApplicationBridge{response: []byte(`{"ok":true}`)}
			h := newBridgeHarness(t).withCaps(definition.capability)
			h.host.ApplicationBridge = bridge
			h.install()
			body := []byte(`{"run_id":"run-1","authority":"forged","idempotency_key":"logical-1"}`)
			h.memWrite(0, body)
			got := h.callImport(context.Background(), definition.name, 0, uint64(len(body)), 1024, 256)
			if got <= 0 {
				t.Fatalf("import returned %d", got)
			}
			if len(bridge.calls) != 1 {
				t.Fatalf("bridge calls=%+v", bridge.calls)
			}
			var gotPayload, wantPayload any
			_ = json.Unmarshal(bridge.calls[0].payload, &gotPayload)
			_ = json.Unmarshal([]byte(`{"run_id":"run-1","authority":"forged"}`), &wantPayload)
			if bridge.calls[0].operation != definition.operation || bridge.calls[0].requestID != "logical-1" || !reflect.DeepEqual(gotPayload, wantPayload) {
				t.Fatalf("bridge calls=%+v", bridge.calls)
			}
		})
	}
}

func TestApplicationImportsFailClosedBeforeBridge(t *testing.T) {
	definition, err := applicationImportForOperation("hold.acquire")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)

	bridge := &recordingApplicationBridge{response: []byte(`{}`)}
	withoutCap := newBridgeHarness(t)
	withoutCap.host.ApplicationBridge = bridge
	withoutCap.install()
	withoutCap.memWrite(0, body)
	if got := withoutCap.callImport(context.Background(), definition.name, 0, uint64(len(body)), 1024, 256); got >= 0 {
		t.Fatalf("missing capability returned %d", got)
	}
	if len(bridge.calls) != 0 {
		t.Fatal("missing capability reached bridge")
	}

	withoutBroker := newBridgeHarness(t).withCaps(definition.capability).install()
	withoutBroker.memWrite(0, body)
	if got := withoutBroker.callImport(context.Background(), definition.name, 0, uint64(len(body)), 1024, 256); got >= 0 {
		t.Fatalf("missing broker returned %d", got)
	}
}

func TestApplicationBridgeErrorIsBoundedGuestError(t *testing.T) {
	definition, _ := applicationImportForOperation("timer.schedule")
	bridge := &recordingApplicationBridge{err: errors.New("private broker detail")}
	h := newBridgeHarness(t).withCaps(definition.capability)
	h.host.ApplicationBridge = bridge
	h.install()
	body := []byte(`{"after_ms":1000}`)
	h.memWrite(0, body)
	got := h.callImport(context.Background(), definition.name, 0, uint64(len(body)), 1024, 64)
	if got >= 0 {
		t.Fatalf("broker error returned %d", got)
	}
	message := h.memRead(1024, uint32(-got))
	if bytes.Contains(message, []byte("private broker detail")) {
		t.Fatalf("broker details leaked to guest: %q", message)
	}
}
