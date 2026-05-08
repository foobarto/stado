package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

func TestPendingChoiceRegistry_AllocateResolveDelivers(t *testing.T) {
	r := newPendingChoiceRegistry()
	id, ch := r.allocate("sess-1", pluginRuntime.ChoiceRequest{})
	if id == "" {
		t.Fatal("empty id")
	}
	go r.resolve(id, pluginRuntime.ChoiceResponse{Selected: []string{"a"}})
	select {
	case got := <-ch:
		if len(got.Selected) != 1 || got.Selected[0] != "a" {
			t.Errorf("delivered = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("response not delivered")
	}
}

func TestPendingChoiceRegistry_ResolveUnknownReturnsFalse(t *testing.T) {
	r := newPendingChoiceRegistry()
	if r.resolve("nope", pluginRuntime.ChoiceResponse{}) {
		t.Error("resolve(nonexistent) should return false")
	}
}

func TestPendingChoiceRegistry_CancelSessionDelivers(t *testing.T) {
	r := newPendingChoiceRegistry()
	idA, chA := r.allocate("sess-1", pluginRuntime.ChoiceRequest{})
	idB, chB := r.allocate("sess-1", pluginRuntime.ChoiceRequest{})
	idC, chC := r.allocate("sess-2", pluginRuntime.ChoiceRequest{})
	r.cancelSession("sess-1")

	for _, p := range []struct {
		id   string
		ch   chan pluginRuntime.ChoiceResponse
		want bool
	}{
		{idA, chA, true},
		{idB, chB, true},
	} {
		select {
		case got := <-p.ch:
			if !got.Cancelled {
				t.Errorf("id %s: cancelled=false, want true", p.id)
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

// TestServerHandleSessionChoiceResponse_Roundtrip drives the
// kind=choice → session/choice_response loop end-to-end via a
// captured connection writer + a synchronous Server. The bridge
// goroutine emits the notification, the test acts as the client by
// dispatching the response RPC, and the bridge returns with the
// operator's pick.
func TestServerHandleSessionChoiceResponse_Roundtrip(t *testing.T) {
	out := newWriterSync()
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), out)

	bridgeResp := make(chan pluginRuntime.ChoiceResponse, 1)
	go func() {
		resp, _ := srv.requestChoice(context.Background(), "sess-1", pluginRuntime.ChoiceRequest{
			Prompt:  "Pick one",
			Options: []pluginRuntime.ChoiceOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Bravo"}},
		})
		bridgeResp <- resp
	}()

	// Spin until the notification lands in the buffer (bridge runs
	// in a goroutine; bound the wait).
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"kind":"choice"`) {
		if time.Now().After(deadline) {
			t.Fatalf("kind=choice notification not seen; buffer: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Extract the request id from the notification.
	var notif Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &notif); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	params := notif.Params.(map[string]any)
	requestID, _ := params["requestId"].(string)
	if requestID == "" {
		t.Fatalf("missing requestId in notification: %+v", params)
	}

	// Act as the client: dispatch session/choice_response.
	respPayload := json.RawMessage(`{"sessionId":"sess-1","requestId":"` + requestID + `","selected":["b"]}`)
	if _, err := srv.handleSessionChoiceResponse(respPayload); err != nil {
		t.Fatalf("handleSessionChoiceResponse: %v", err)
	}
	select {
	case got := <-bridgeResp:
		if got.Cancelled || len(got.Selected) != 1 || got.Selected[0] != "b" {
			t.Errorf("bridge response = %+v, want selected=[b]", got)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not return after response")
	}
}

// TestServerHandleSessionChoiceResponse_UnknownIDErrors covers the
// malicious / late-response path: a response carrying an unknown
// request id surfaces as a CodeInvalidParams error so the client can
// detect the de-sync.
func TestServerHandleSessionChoiceResponse_UnknownIDErrors(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), io.Discard)
	resp := json.RawMessage(`{"sessionId":"sess-1","requestId":"nope","selected":[]}`)
	_, err := srv.handleSessionChoiceResponse(resp)
	if err == nil {
		t.Fatal("expected error for unknown requestId")
	}
	if rpcErr, ok := err.(*RPCError); !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams", err)
	}
}

// TestServerHandleSessionChoiceResponse_F10InputFields_RoundTrip:
// the F10 ACP follow-on extends the wire format with per-option
// prefix + input + validator on the outbound notification, and
// inputValue on the inbound response. Asserts both directions
// flow through and the bridge response carries InputValue back to
// the plugin.
func TestServerHandleSessionChoiceResponse_F10InputFields_RoundTrip(t *testing.T) {
	out := newWriterSync()
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), out)

	bridgeResp := make(chan pluginRuntime.ChoiceResponse, 1)
	go func() {
		resp, _ := srv.requestChoice(context.Background(), "sess-1", pluginRuntime.ChoiceRequest{
			Prompt: "Run with what model?",
			Options: []pluginRuntime.ChoiceOption{
				{
					ID: "go", Label: "Run", Prefix: "model:",
					Input: &pluginRuntime.ChoiceInput{
						Default: "claude-sonnet-4-6",
					},
				},
				{ID: "skip", Label: "Skip"},
			},
		})
		bridgeResp <- resp
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"kind":"choice"`) {
		if time.Now().After(deadline) {
			t.Fatalf("notification not seen; buffer: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wire format assertion: outbound notification carries the new
	// prefix + input fields. Trim and parse.
	var notif Notification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &notif); err != nil {
		t.Fatalf("notification json: %v\n%s", err, out.String())
	}
	params := notif.Params.(map[string]any)
	options, _ := params["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("options len = %d, want 2", len(options))
	}
	first, _ := options[0].(map[string]any)
	if first["prefix"] != "model:" {
		t.Errorf("first.prefix = %v, want %q", first["prefix"], "model:")
	}
	input, ok := first["input"].(map[string]any)
	if !ok {
		t.Fatalf("first.input not present or wrong type: %v", first["input"])
	}
	if input["default"] != "claude-sonnet-4-6" {
		t.Errorf("first.input.default = %v", input["default"])
	}

	requestID, _ := params["requestId"].(string)
	respPayload := json.RawMessage(`{"sessionId":"sess-1","requestId":"` + requestID +
		`","selected":["go"],"inputValue":"claude-opus-4-7"}`)
	if _, err := srv.handleSessionChoiceResponse(respPayload); err != nil {
		t.Fatalf("handleSessionChoiceResponse: %v", err)
	}
	select {
	case got := <-bridgeResp:
		if got.Cancelled {
			t.Errorf("cancelled = true, want false")
		}
		if len(got.Selected) != 1 || got.Selected[0] != "go" {
			t.Errorf("selected = %v, want [go]", got.Selected)
		}
		if got.InputValue != "claude-opus-4-7" {
			t.Errorf("input_value = %q, want %q", got.InputValue, "claude-opus-4-7")
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not return after response")
	}
}

// TestServerHandleSessionChoiceResponse_F10InputValidatorRejectsAndAllowsRetry:
// when the chosen option carries a validator and the client sends
// input_value that fails it, the server returns an RPC error and
// LEAVES the pending entry in the registry so the client can retry
// with corrected input. Asserts both halves: failure surface +
// successful retry against the same requestId.
func TestServerHandleSessionChoiceResponse_F10InputValidatorRejectsAndAllowsRetry(t *testing.T) {
	out := newWriterSync()
	srv := NewServer(nil, nil)
	srv.conn = NewConn(strings.NewReader(""), out)

	bridgeResp := make(chan pluginRuntime.ChoiceResponse, 1)
	go func() {
		resp, _ := srv.requestChoice(context.Background(), "sess-1", pluginRuntime.ChoiceRequest{
			Prompt: "Turns?",
			Options: []pluginRuntime.ChoiceOption{
				{
					ID: "n", Label: "Run with budget",
					Input: &pluginRuntime.ChoiceInput{
						Default:   "5",
						Validator: &pluginRuntime.ChoiceValidator{Kind: "int"},
					},
				},
			},
		})
		bridgeResp <- resp
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"kind":"choice"`) {
		if time.Now().After(deadline) {
			t.Fatalf("notification not seen; buffer: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var notif Notification
	_ = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &notif)
	requestID, _ := notif.Params.(map[string]any)["requestId"].(string)

	// First attempt: input_value="abc" fails the int validator.
	bad := json.RawMessage(`{"sessionId":"sess-1","requestId":"` + requestID +
		`","selected":["n"],"inputValue":"abc"}`)
	_, err := srv.handleSessionChoiceResponse(bad)
	if err == nil {
		t.Fatal("expected RPC error for validator failure")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != CodeInvalidParams {
		t.Errorf("err = %v, want CodeInvalidParams RPCError", err)
	}
	if !strings.Contains(err.Error(), "integer") {
		t.Errorf("err = %q, should mention integer validator", err.Error())
	}

	// Bridge must NOT have returned — entry stays in registry.
	select {
	case got := <-bridgeResp:
		t.Fatalf("bridge resolved on validator failure: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	// Retry with valid input_value resolves cleanly.
	good := json.RawMessage(`{"sessionId":"sess-1","requestId":"` + requestID +
		`","selected":["n"],"inputValue":"7"}`)
	if _, err := srv.handleSessionChoiceResponse(good); err != nil {
		t.Fatalf("retry: %v", err)
	}
	select {
	case got := <-bridgeResp:
		if got.InputValue != "7" {
			t.Errorf("input_value = %q, want 7", got.InputValue)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not return after valid retry")
	}
}

// writerSync wraps a bytes.Buffer with a mutex so the bridge
// goroutine's Write and the test's read loop don't race. Provides
// String() / Bytes() that take the same lock so callers don't have
// to reach into the inner buffer themselves (which is what was
// triggering -race failures).
type writerSync struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func newWriterSync() *writerSync {
	return &writerSync{w: &bytes.Buffer{}}
}

func (w *writerSync) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// String returns a snapshot of the buffer's current contents under
// the same lock that guards Write, so race detector is satisfied.
func (w *writerSync) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.String()
}

// Bytes returns a copy of the buffer's current contents under the
// same lock as Write.
func (w *writerSync) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.w.Bytes()...)
}
