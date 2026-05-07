package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingMemoryBridge is a fake MemoryBridge that records calls
// + returns programmable results. Mirrors recordingSessionBridge.
type recordingMemoryBridge struct {
	mu sync.Mutex

	proposeCalls atomic.Int32
	queryCalls   atomic.Int32
	updateCalls  atomic.Int32

	lastProposePayload []byte
	lastQueryPayload   []byte
	lastUpdatePayload  []byte

	queryResult []byte

	proposeErr error
	queryErr   error
	updateErr  error

	blockPropose bool
	blockQuery   bool
	blockUpdate  bool

	lastProposeCtx context.Context
	lastQueryCtx   context.Context
	lastUpdateCtx  context.Context
}

func (b *recordingMemoryBridge) Propose(ctx context.Context, payload []byte) error {
	b.proposeCalls.Add(1)
	b.mu.Lock()
	b.lastProposeCtx = ctx
	b.lastProposePayload = append([]byte(nil), payload...)
	block := b.blockPropose
	err := b.proposeErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (b *recordingMemoryBridge) Query(ctx context.Context, payload []byte) ([]byte, error) {
	b.queryCalls.Add(1)
	b.mu.Lock()
	b.lastQueryCtx = ctx
	b.lastQueryPayload = append([]byte(nil), payload...)
	block := b.blockQuery
	res := b.queryResult
	err := b.queryErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return res, err
}

func (b *recordingMemoryBridge) Update(ctx context.Context, payload []byte) error {
	b.updateCalls.Add(1)
	b.mu.Lock()
	b.lastUpdateCtx = ctx
	b.lastUpdatePayload = append([]byte(nil), payload...)
	block := b.blockUpdate
	err := b.updateErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

// ---- Contract 1: capability gate ---------------------------------------

func TestMemoryBridge_CapGate_DeniesEveryImportWithoutCap(t *testing.T) {
	br := &recordingMemoryBridge{}
	h := newBridgeHarness(t).
		withMemoryBridge(br). // bridge wired; gate must deny first
		install()

	cases := []struct {
		name string
		args []uint64
	}{
		{"stado_memory_propose", []uint64{0, 0}},
		{"stado_memory_query", []uint64{0, 0, 0, 0}},
		{"stado_memory_update", []uint64{0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.callImport(context.Background(), c.name, c.args...)
			if got != -1 {
				t.Errorf("%s with no cap: got %d, want -1", c.name, got)
			}
		})
	}
	if n := br.proposeCalls.Load() + br.queryCalls.Load() + br.updateCalls.Load(); n != 0 {
		t.Errorf("bridge invoked while caps denied: counters total=%d", n)
	}
}

// ---- Contract 2: nil-bridge --------------------------------------------

func TestMemoryBridge_NilBridge_AllImportsReturnSentinel(t *testing.T) {
	h := newBridgeHarness(t).
		withCaps("memory:propose", "memory:read", "memory:write").
		install()

	cases := []struct {
		name string
		args []uint64
	}{
		{"stado_memory_propose", []uint64{0, 0}},
		{"stado_memory_query", []uint64{0, 0, 0, 0}},
		{"stado_memory_update", []uint64{0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.callImport(context.Background(), c.name, c.args...)
			if got != -1 {
				t.Errorf("%s with nil bridge: got %d, want -1", c.name, got)
			}
		})
	}
}

// ---- Contract 3: exact forwarding --------------------------------------

func TestMemoryBridge_Forwarding_Propose(t *testing.T) {
	br := &recordingMemoryBridge{}
	h := newBridgeHarness(t).
		withCaps("memory:propose").
		withMemoryBridge(br).
		install()

	payload := []byte(`{"id":"mem-1","summary":"hello"}`)
	h.memWrite(0, payload)

	got := h.callImport(context.Background(), "stado_memory_propose",
		0, uint64(len(payload)))
	if got != 0 {
		t.Errorf("expected 0 on success, got %d", got)
	}
	if br.proposeCalls.Load() != 1 {
		t.Errorf("proposeCalls = %d, want 1", br.proposeCalls.Load())
	}
	if string(br.lastProposePayload) != string(payload) {
		t.Errorf("forwarded payload = %q, want %q", br.lastProposePayload, payload)
	}
}

func TestMemoryBridge_Forwarding_Query(t *testing.T) {
	want := []byte(`{"items":[{"id":"mem-1"}]}`)
	br := &recordingMemoryBridge{queryResult: want}
	h := newBridgeHarness(t).
		withCaps("memory:read").
		withMemoryBridge(br).
		install()

	query := []byte(`{"prompt":"recent"}`)
	h.memWrite(0, query)
	const outPtr, outCap = 256, 256

	n := h.callImport(context.Background(), "stado_memory_query",
		0, uint64(len(query)),
		outPtr, outCap)
	if n != int32(len(want)) {
		t.Fatalf("got n=%d, want %d", n, len(want))
	}
	if string(br.lastQueryPayload) != string(query) {
		t.Errorf("forwarded query = %q, want %q", br.lastQueryPayload, query)
	}
	if got := h.memRead(outPtr, uint32(n)); string(got) != string(want) {
		t.Errorf("output buffer = %q, want %q", got, want)
	}
}

func TestMemoryBridge_Forwarding_Update(t *testing.T) {
	br := &recordingMemoryBridge{}
	h := newBridgeHarness(t).
		withCaps("memory:write").
		withMemoryBridge(br).
		install()

	payload := []byte(`{"action":"approve","id":"mem-1"}`)
	h.memWrite(0, payload)

	got := h.callImport(context.Background(), "stado_memory_update",
		0, uint64(len(payload)))
	if got != 0 {
		t.Errorf("expected 0 on success, got %d", got)
	}
	if string(br.lastUpdatePayload) != string(payload) {
		t.Errorf("forwarded payload = %q, want %q", br.lastUpdatePayload, payload)
	}
}

// ---- Contract 4: cancel propagation ------------------------------------

func TestMemoryBridge_Cancel_PropagatesToPropose(t *testing.T) {
	br := &recordingMemoryBridge{blockPropose: true}
	h := newBridgeHarness(t).
		withCaps("memory:propose").
		withMemoryBridge(br).
		install()

	payload := []byte(`{}`)
	h.memWrite(0, payload)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_memory_propose")
	_, _ = fn.Call(ctx, 0, uint64(len(payload)))

	if br.proposeCalls.Load() != 1 {
		t.Errorf("proposeCalls = %d, want 1", br.proposeCalls.Load())
	}
	if br.lastProposeCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastProposeCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastProposeCtx.Err(), context.Canceled) {
		t.Errorf("propose ctx not cancelled: %v", br.lastProposeCtx.Err())
	}
}

func TestMemoryBridge_Cancel_PropagatesToQuery(t *testing.T) {
	br := &recordingMemoryBridge{blockQuery: true}
	h := newBridgeHarness(t).
		withCaps("memory:read").
		withMemoryBridge(br).
		install()

	payload := []byte(`{}`)
	h.memWrite(0, payload)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_memory_query")
	_, _ = fn.Call(ctx, 0, uint64(len(payload)), 256, 256)

	if br.queryCalls.Load() != 1 {
		t.Errorf("queryCalls = %d, want 1", br.queryCalls.Load())
	}
	if br.lastQueryCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastQueryCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastQueryCtx.Err(), context.Canceled) {
		t.Errorf("query ctx not cancelled: %v", br.lastQueryCtx.Err())
	}
}

func TestMemoryBridge_Cancel_PropagatesToUpdate(t *testing.T) {
	br := &recordingMemoryBridge{blockUpdate: true}
	h := newBridgeHarness(t).
		withCaps("memory:write").
		withMemoryBridge(br).
		install()

	payload := []byte(`{}`)
	h.memWrite(0, payload)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_memory_update")
	_, _ = fn.Call(ctx, 0, uint64(len(payload)))

	if br.updateCalls.Load() != 1 {
		t.Errorf("updateCalls = %d, want 1", br.updateCalls.Load())
	}
	if br.lastUpdateCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastUpdateCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastUpdateCtx.Err(), context.Canceled) {
		t.Errorf("update ctx not cancelled: %v", br.lastUpdateCtx.Err())
	}
}
