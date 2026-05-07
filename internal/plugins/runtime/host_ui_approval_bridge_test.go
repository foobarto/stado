package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingApprovalBridge records calls and returns programmable
// outcomes. ApprovalBridge.RequestApproval is the only method.
type recordingApprovalBridge struct {
	mu sync.Mutex

	calls atomic.Int32

	lastTitle string
	lastBody  string
	lastCtx   context.Context

	allow bool
	err   error
	block bool
}

func (b *recordingApprovalBridge) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	b.calls.Add(1)
	b.mu.Lock()
	b.lastCtx = ctx
	b.lastTitle = title
	b.lastBody = body
	block := b.block
	allow := b.allow
	err := b.err
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return allow, err
}

// ---- Contract 1: capability gate ---------------------------------------

func TestApprovalBridge_CapGate_DeniedWithoutCap(t *testing.T) {
	br := &recordingApprovalBridge{allow: true}
	h := newBridgeHarness(t).
		withApprovalBridge(br).
		install()

	got := h.callImport(context.Background(), "stado_ui_approve", 0, 0, 0, 0)
	if got != -1 {
		t.Errorf("got %d, want -1", got)
	}
	if br.calls.Load() != 0 {
		t.Errorf("bridge invoked while cap denied: calls=%d", br.calls.Load())
	}
}

// ---- Contract 2: nil-bridge --------------------------------------------

func TestApprovalBridge_NilBridge_ReturnsSentinel(t *testing.T) {
	h := newBridgeHarness(t).
		withCaps("ui:approval").
		install()

	got := h.callImport(context.Background(), "stado_ui_approve", 0, 0, 0, 0)
	if got != -1 {
		t.Errorf("got %d, want -1", got)
	}
}

// ---- Contract 3: exact forwarding --------------------------------------

func TestApprovalBridge_Forwarding_AllowEncodesAs1(t *testing.T) {
	br := &recordingApprovalBridge{allow: true}
	h := newBridgeHarness(t).
		withCaps("ui:approval").
		withApprovalBridge(br).
		install()

	title := []byte("Run script?")
	body := []byte("This will execute foo.sh")
	h.memWrite(0, title)
	h.memWrite(64, body)

	got := h.callImport(context.Background(), "stado_ui_approve",
		0, uint64(len(title)),
		64, uint64(len(body)))
	if got != 1 {
		t.Errorf("approved → expected 1, got %d", got)
	}
	if br.lastTitle != string(title) {
		t.Errorf("forwarded title = %q, want %q", br.lastTitle, title)
	}
	if br.lastBody != string(body) {
		t.Errorf("forwarded body = %q, want %q", br.lastBody, body)
	}
}

func TestApprovalBridge_Forwarding_DenyEncodesAs0(t *testing.T) {
	br := &recordingApprovalBridge{allow: false}
	h := newBridgeHarness(t).
		withCaps("ui:approval").
		withApprovalBridge(br).
		install()

	title := []byte("ok?")
	body := []byte("body")
	h.memWrite(0, title)
	h.memWrite(64, body)

	got := h.callImport(context.Background(), "stado_ui_approve",
		0, uint64(len(title)),
		64, uint64(len(body)))
	if got != 0 {
		t.Errorf("denied → expected 0, got %d", got)
	}
}

func TestApprovalBridge_Forwarding_BridgeErrorMapsToSentinel(t *testing.T) {
	br := &recordingApprovalBridge{err: errors.New("operator quit")}
	h := newBridgeHarness(t).
		withCaps("ui:approval").
		withApprovalBridge(br).
		install()

	title := []byte("t")
	body := []byte("b")
	h.memWrite(0, title)
	h.memWrite(64, body)

	got := h.callImport(context.Background(), "stado_ui_approve",
		0, uint64(len(title)),
		64, uint64(len(body)))
	if got != -1 {
		t.Errorf("bridge error → expected -1, got %d", got)
	}
}

// ---- Contract 4: cancel propagation ------------------------------------

func TestApprovalBridge_Cancel_PropagatesToBridge(t *testing.T) {
	br := &recordingApprovalBridge{block: true}
	h := newBridgeHarness(t).
		withCaps("ui:approval").
		withApprovalBridge(br).
		install()

	title := []byte("t")
	body := []byte("b")
	h.memWrite(0, title)
	h.memWrite(64, body)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_ui_approve")
	_, _ = fn.Call(ctx,
		0, uint64(len(title)),
		64, uint64(len(body)))

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
