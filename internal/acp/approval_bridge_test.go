package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPendingApprovalRegistry_AllocateResolveDelivers(t *testing.T) {
	r := newPendingApprovalRegistry()
	id, ch := r.allocate("sess-1")
	if id == "" {
		t.Fatal("empty id")
	}
	go r.resolve(id, approvalOutcome{allow: true})
	select {
	case got := <-ch:
		if !got.allow || got.cancelled {
			t.Errorf("delivered = %+v, want allow=true cancelled=false", got)
		}
	case <-time.After(time.Second):
		t.Fatal("response not delivered")
	}
}

func TestPendingApprovalRegistry_ResolveUnknownReturnsFalse(t *testing.T) {
	r := newPendingApprovalRegistry()
	if r.resolve("nope", approvalOutcome{allow: true}) {
		t.Error("resolve(nonexistent) should return false")
	}
}

func TestPendingApprovalRegistry_CancelSessionDelivers(t *testing.T) {
	r := newPendingApprovalRegistry()
	idA, chA := r.allocate("sess-1")
	idB, chB := r.allocate("sess-1")
	idC, chC := r.allocate("sess-2")
	r.cancelSession("sess-1")

	for _, p := range []struct {
		id string
		ch chan approvalOutcome
	}{
		{idA, chA},
		{idB, chB},
	} {
		select {
		case got := <-p.ch:
			if !got.cancelled || got.allow {
				t.Errorf("id %s: outcome %+v, want cancelled=true allow=false", p.id, got)
			}
		case <-time.After(time.Second):
			t.Errorf("id %s: cancel not delivered", p.id)
		}
	}
	// sess-2 entry should still be alive — cancel was scoped.
	select {
	case got := <-chC:
		t.Errorf("sess-2 entry id %s incorrectly cancelled: %+v", idC, got)
	default:
	}
}

// TestServerHandleSessionApprovalResponse_Roundtrip drives the
// kind=approval → session/approval_response loop end-to-end via a
// captured connection writer + a synchronous Server. Mirrors the
// choice-bridge roundtrip test.
func TestServerHandleSessionApprovalResponse_Roundtrip(t *testing.T) {
	out := newWriterSync()
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), out)

	type bridgeResult struct {
		allow bool
		err   error
	}
	bridgeCh := make(chan bridgeResult, 1)
	go func() {
		allow, err := srv.requestApproval(context.Background(), "sess-1", "Run risky thing", "tool will format /")
		bridgeCh <- bridgeResult{allow: allow, err: err}
	}()

	// Spin until the notification lands in the buffer (bridge runs
	// in a goroutine; bound the wait).
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"kind":"approval"`) {
		if time.Now().After(deadline) {
			t.Fatalf("kind=approval notification not seen; buffer: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var notif Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &notif); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	params := notif.Params.(map[string]any)
	requestID, _ := params["requestId"].(string)
	if requestID == "" {
		t.Fatalf("missing requestId in notification: %+v", params)
	}
	if title, _ := params["title"].(string); title != "Run risky thing" {
		t.Errorf("title = %q, want %q", title, "Run risky thing")
	}
	if body, _ := params["body"].(string); body != "tool will format /" {
		t.Errorf("body = %q, want %q", body, "tool will format /")
	}

	// Act as the client: dispatch session/approval_response with allow=true.
	respPayload := json.RawMessage(`{"sessionId":"sess-1","requestId":"` + requestID + `","allow":true}`)
	if _, err := srv.handleSessionApprovalResponse(respPayload); err != nil {
		t.Fatalf("handleSessionApprovalResponse: %v", err)
	}
	select {
	case got := <-bridgeCh:
		if got.err != nil {
			t.Fatalf("bridge err = %v", got.err)
		}
		if !got.allow {
			t.Errorf("bridge allow = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not return after response")
	}
}

// TestServerHandleSessionApprovalResponse_CancelledCollapsesToDeny
// covers the operator-dismissed path. cancelled=true must surface as
// allow=false at the bridge boundary, regardless of what the client
// put in the allow field.
func TestServerHandleSessionApprovalResponse_CancelledCollapsesToDeny(t *testing.T) {
	out := newWriterSync()
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), out)

	type bridgeResult struct {
		allow bool
		err   error
	}
	bridgeCh := make(chan bridgeResult, 1)
	go func() {
		allow, err := srv.requestApproval(context.Background(), "sess-1", "T", "B")
		bridgeCh <- bridgeResult{allow: allow, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"requestId"`) {
		if time.Now().After(deadline) {
			t.Fatalf("notification not seen; buffer: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var notif Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &notif); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	requestID, _ := notif.Params.(map[string]any)["requestId"].(string)

	// Client claims allow=true but cancelled=true — bridge MUST see deny.
	respPayload := json.RawMessage(`{"sessionId":"sess-1","requestId":"` + requestID + `","allow":true,"cancelled":true}`)
	if _, err := srv.handleSessionApprovalResponse(respPayload); err != nil {
		t.Fatalf("handleSessionApprovalResponse: %v", err)
	}
	select {
	case got := <-bridgeCh:
		if got.allow {
			t.Errorf("cancelled response leaked through as allow=true; bridge must collapse to false")
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not return after response")
	}
}

func TestServerHandleSessionApprovalResponse_UnknownIDErrors(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	resp := json.RawMessage(`{"sessionId":"sess-1","requestId":"nope","allow":true}`)
	_, err := srv.handleSessionApprovalResponse(resp)
	if err == nil {
		t.Fatal("expected error for unknown requestId")
	}
	if rpcErr, ok := err.(*RPCError); !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
}

// TestServerHandleSessionApprovalResponse_MissingRequestIDErrors covers
// the malformed-client-message path: an empty requestId is rejected
// before touching the registry.
func TestServerHandleSessionApprovalResponse_MissingRequestIDErrors(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	resp := json.RawMessage(`{"sessionId":"sess-1","allow":true}`)
	_, err := srv.handleSessionApprovalResponse(resp)
	if err == nil {
		t.Fatal("expected error for missing requestId")
	}
	if rpcErr, ok := err.(*RPCError); !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
}
