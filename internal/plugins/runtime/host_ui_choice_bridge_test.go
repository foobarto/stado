package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingChoiceBridge records calls and returns programmable
// outcomes. ChoiceBridge.RequestChoice is the only method.
//
// Note on stado_ui_choose's return convention: unlike most bridges,
// it returns *positive* bytes-written on success, *negative*
// bytes-written for an error message staged at the response buffer
// (encodeToolSidePayload). Tests assert the sign + the message
// content where relevant.
type recordingChoiceBridge struct {
	mu sync.Mutex

	calls atomic.Int32

	lastReq ChoiceRequest
	lastCtx context.Context

	resp ChoiceResponse
	err  error

	block bool
}

func (b *recordingChoiceBridge) RequestChoice(ctx context.Context, req ChoiceRequest) (ChoiceResponse, error) {
	b.calls.Add(1)
	b.mu.Lock()
	b.lastCtx = ctx
	b.lastReq = req
	block := b.block
	resp := b.resp
	err := b.err
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return ChoiceResponse{}, ctx.Err()
	}
	return resp, err
}

// ---- Contract 1: capability gate ---------------------------------------

func TestChoiceBridge_CapGate_DeniedWithoutCap(t *testing.T) {
	br := &recordingChoiceBridge{}
	h := newBridgeHarness(t).
		withChoiceBridge(br).
		install()

	const respPtr, respCap = 0, 256
	got := h.callImport(context.Background(), "stado_ui_choose",
		0, 0, respPtr, respCap)
	if got >= 0 {
		t.Errorf("cap denied → expected negative, got %d", got)
	}
	if br.calls.Load() != 0 {
		t.Errorf("bridge invoked while cap denied: calls=%d", br.calls.Load())
	}
	// The error message at resp_ptr should mention the cap.
	msg := h.memRead(respPtr, uint32(-got))
	if string(msg) != "ui:choice cap missing" {
		t.Errorf("error msg = %q, want %q", msg, "ui:choice cap missing")
	}
}

// ---- Contract 2: nil-bridge --------------------------------------------

func TestChoiceBridge_NilBridge_ReturnsUnavailable(t *testing.T) {
	h := newBridgeHarness(t).
		withCaps("ui:choice").
		install()

	const respPtr, respCap = 0, 256
	got := h.callImport(context.Background(), "stado_ui_choose",
		0, 0, respPtr, respCap)
	if got >= 0 {
		t.Errorf("nil bridge → expected negative, got %d", got)
	}
	msg := h.memRead(respPtr, uint32(-got))
	if string(msg) != "interactive UI unavailable" {
		t.Errorf("error msg = %q, want %q", msg, "interactive UI unavailable")
	}
}

// ---- Contract 3: exact forwarding --------------------------------------

func TestChoiceBridge_Forwarding_SingleSelect(t *testing.T) {
	br := &recordingChoiceBridge{
		resp: ChoiceResponse{Selected: []string{"a"}},
	}
	h := newBridgeHarness(t).
		withCaps("ui:choice").
		withChoiceBridge(br).
		install()

	req := chooseRequestWire{
		Prompt: "Pick one",
		Options: []chooseOptionWire{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Bravo"},
		},
		Multi:   false,
		Default: []string{"a"},
	}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)
	const respPtr, respCap = 256, 256

	n := h.callImport(context.Background(), "stado_ui_choose",
		0, uint64(len(reqBytes)),
		respPtr, respCap)
	if n <= 0 {
		t.Fatalf("success → expected positive, got %d", n)
	}
	// Forwarded request reaches the bridge intact.
	if br.lastReq.Prompt != "Pick one" {
		t.Errorf("forwarded prompt = %q, want %q", br.lastReq.Prompt, "Pick one")
	}
	if len(br.lastReq.Options) != 2 ||
		br.lastReq.Options[0].ID != "a" ||
		br.lastReq.Options[1].ID != "b" {
		t.Errorf("forwarded options = %+v, want a/b", br.lastReq.Options)
	}
	if br.lastReq.Multi {
		t.Errorf("Multi forwarded as true; want false")
	}
	if len(br.lastReq.Default) != 1 || br.lastReq.Default[0] != "a" {
		t.Errorf("forwarded Default = %v, want [a]", br.lastReq.Default)
	}
	// Bridge response round-trips back to wasm memory.
	out := h.memRead(respPtr, uint32(n))
	var resp chooseResponseWire
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v (raw=%s)", err, out)
	}
	if len(resp.Selected) != 1 || resp.Selected[0] != "a" {
		t.Errorf("response Selected = %v, want [a]", resp.Selected)
	}
	if resp.Cancelled {
		t.Errorf("response Cancelled = true; want false")
	}
}

func TestChoiceBridge_Forwarding_MultiSelect(t *testing.T) {
	br := &recordingChoiceBridge{
		resp: ChoiceResponse{Selected: []string{"a", "c"}},
	}
	h := newBridgeHarness(t).
		withCaps("ui:choice").
		withChoiceBridge(br).
		install()

	req := chooseRequestWire{
		Prompt: "Pick many",
		Options: []chooseOptionWire{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Bravo"},
			{ID: "c", Label: "Charlie"},
		},
		Multi: true,
	}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)
	const respPtr, respCap = 256, 256

	n := h.callImport(context.Background(), "stado_ui_choose",
		0, uint64(len(reqBytes)),
		respPtr, respCap)
	if n <= 0 {
		t.Fatalf("success → expected positive, got %d", n)
	}
	if !br.lastReq.Multi {
		t.Errorf("Multi forwarded as false; want true")
	}
	out := h.memRead(respPtr, uint32(n))
	var resp chooseResponseWire
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Selected) != 2 || resp.Selected[0] != "a" || resp.Selected[1] != "c" {
		t.Errorf("Selected = %v, want [a c]", resp.Selected)
	}
}

func TestChoiceBridge_Forwarding_CancelledResponse(t *testing.T) {
	br := &recordingChoiceBridge{
		resp: ChoiceResponse{Cancelled: true},
	}
	h := newBridgeHarness(t).
		withCaps("ui:choice").
		withChoiceBridge(br).
		install()

	req := chooseRequestWire{
		Prompt:  "Pick",
		Options: []chooseOptionWire{{ID: "a", Label: "A"}},
	}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)
	const respPtr, respCap = 256, 256

	n := h.callImport(context.Background(), "stado_ui_choose",
		0, uint64(len(reqBytes)),
		respPtr, respCap)
	if n <= 0 {
		t.Fatalf("expected positive, got %d", n)
	}
	out := h.memRead(respPtr, uint32(n))
	var resp chooseResponseWire
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Cancelled {
		t.Errorf("Cancelled = false; want true (operator dismissed)")
	}
	// Selected is non-nil empty slice — round-trips as [] in JSON.
	if len(resp.Selected) != 0 {
		t.Errorf("Selected = %v, want empty", resp.Selected)
	}
}

// ---- Contract 4: cancel propagation ------------------------------------

func TestChoiceBridge_Cancel_PropagatesToBridge(t *testing.T) {
	br := &recordingChoiceBridge{block: true}
	h := newBridgeHarness(t).
		withCaps("ui:choice").
		withChoiceBridge(br).
		install()

	req := chooseRequestWire{
		Prompt:  "blocked",
		Options: []chooseOptionWire{{ID: "a", Label: "A"}},
	}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_ui_choose")
	_, _ = fn.Call(ctx, 0, uint64(len(reqBytes)), 256, 256)

	if br.calls.Load() != 1 {
		t.Errorf("bridge calls = %d, want 1", br.calls.Load())
	}
	if br.lastCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastCtx.Err(), context.Canceled) {
		t.Errorf("ctx not cancelled: %v", br.lastCtx.Err())
	}
}
