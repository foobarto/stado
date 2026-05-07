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

// recordingSessionBridge is a fake SessionBridge that records calls
// + returns programmable results. Used to assert the four bridge
// contracts (capability gate / nil / forwarding / cancel) on every
// stado_session_* host import.
type recordingSessionBridge struct {
	mu sync.Mutex

	// Call counters — each contract test asserts these stay at zero
	// when the gate denies, or increment when the gate permits.
	readCalls  atomic.Int32
	eventCalls atomic.Int32
	forkCalls  atomic.Int32
	llmCalls   atomic.Int32

	// Last-call args (forwarding contract: assert the import passes
	// arguments verbatim to the bridge).
	lastReadField string
	lastForkAt    string
	lastForkSeed  string
	lastLLMPrompt string
	lastLLMOpts   LLMInvokeOpts

	// Programmable outputs.
	readResult  []byte
	eventResult []byte
	forkResult  string
	llmReply    string
	llmTokens   int

	// readErr / eventErr / forkErr / llmErr let tests force the
	// bridge to surface an error.
	readErr  error
	eventErr error
	forkErr  error
	llmErr   error

	// blockEvent / blockFork / blockLLM make the corresponding
	// method block on its ctx. Used by the cancel-propagation
	// contract: tests cancel the ctx and expect the bridge call to
	// return ctx.Err().
	blockEvent bool
	blockFork  bool
	blockLLM   bool

	// lastEventCtx / lastForkCtx / lastLLMCtx carry the ctx the
	// bridge was invoked with so cancel-propagation tests can
	// verify the same ctx the import received reached the bridge.
	lastEventCtx context.Context
	lastForkCtx  context.Context
	lastLLMCtx   context.Context
}

func (b *recordingSessionBridge) NextEvent(ctx context.Context) ([]byte, error) {
	b.eventCalls.Add(1)
	b.mu.Lock()
	b.lastEventCtx = ctx
	block := b.blockEvent
	res := b.eventResult
	err := b.eventErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return res, err
}

func (b *recordingSessionBridge) ReadField(name string) ([]byte, error) {
	b.readCalls.Add(1)
	b.mu.Lock()
	b.lastReadField = name
	res := b.readResult
	err := b.readErr
	b.mu.Unlock()
	return res, err
}

func (b *recordingSessionBridge) Fork(ctx context.Context, atTurn, seed string) (string, error) {
	b.forkCalls.Add(1)
	b.mu.Lock()
	b.lastForkCtx = ctx
	b.lastForkAt = atTurn
	b.lastForkSeed = seed
	block := b.blockFork
	res := b.forkResult
	err := b.forkErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return res, err
}

func (b *recordingSessionBridge) InvokeLLM(ctx context.Context, prompt string, opts LLMInvokeOpts) (string, int, error) {
	b.llmCalls.Add(1)
	b.mu.Lock()
	b.lastLLMCtx = ctx
	b.lastLLMPrompt = prompt
	b.lastLLMOpts = opts
	block := b.blockLLM
	res := b.llmReply
	tok := b.llmTokens
	err := b.llmErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return "", 0, ctx.Err()
	}
	return res, tok, err
}

// ---- Contract 1: capability gate ---------------------------------------
//
// When the manifest doesn't carry the required cap, the host import
// must return -1 without invoking the bridge. The recorder's call
// counters stay at zero.

func TestSessionBridge_CapGate_DeniesEveryImportWithoutCap(t *testing.T) {
	br := &recordingSessionBridge{}
	h := newBridgeHarness(t).
		withSessionBridge(br). // bridge IS wired; gate must deny first
		install()

	cases := []struct {
		name string
		args []uint64
	}{
		{"stado_session_read", []uint64{0, 0, 0, 0}},
		{"stado_session_next_event", []uint64{0, 0}},
		{"stado_session_fork", []uint64{0, 0, 0, 0, 0, 0}},
		{"stado_llm_invoke", []uint64{0, 0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.callImport(context.Background(), c.name, c.args...)
			if got != -1 {
				t.Errorf("%s with no cap: got %d, want -1", c.name, got)
			}
		})
	}
	if n := br.readCalls.Load() + br.eventCalls.Load() + br.forkCalls.Load() + br.llmCalls.Load(); n != 0 {
		t.Errorf("bridge invoked while caps denied: counters total=%d", n)
	}
}

// ---- Contract 2: nil-bridge --------------------------------------------
//
// When the cap is granted but the bridge field is nil, the host
// import must still return the unavailable sentinel (-1) instead of
// nil-derefing.

func TestSessionBridge_NilBridge_AllImportsReturnSentinel(t *testing.T) {
	h := newBridgeHarness(t).
		withCaps("session:read", "session:observe", "session:fork", "llm:invoke").
		// no bridge wired — host.SessionBridge stays nil
		install()

	cases := []struct {
		name string
		args []uint64
	}{
		{"stado_session_read", []uint64{0, 0, 0, 0}},
		{"stado_session_next_event", []uint64{0, 0}},
		{"stado_session_fork", []uint64{0, 0, 0, 0, 0, 0}},
		{"stado_llm_invoke", []uint64{0, 0, 0, 0}},
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
//
// When cap + bridge are both present, the import passes its
// arguments verbatim to the bridge and returns the bridge's result
// bytes back through the output buffer. Tests stage payloads in the
// thunk module's exported memory and assert the recorder sees the
// exact same string + the output bytes match what the recorder
// returned.

func TestSessionBridge_Forwarding_SessionRead(t *testing.T) {
	want := []byte("42")
	br := &recordingSessionBridge{readResult: want}
	h := newBridgeHarness(t).
		withCaps("session:read").
		withSessionBridge(br).
		install()

	// Stage the field name "message_count" at offset 0.
	field := []byte("message_count")
	h.memWrite(0, field)
	// Output buffer at offset 256, capacity 64.
	const outPtr, outCap = 256, 64

	n := h.callImport(context.Background(), "stado_session_read",
		0, uint64(len(field)),
		outPtr, outCap)
	if n != int32(len(want)) {
		t.Fatalf("got n=%d, want %d", n, len(want))
	}
	if br.readCalls.Load() != 1 {
		t.Errorf("bridge readCalls = %d, want 1", br.readCalls.Load())
	}
	if br.lastReadField != "message_count" {
		t.Errorf("forwarded field = %q, want %q", br.lastReadField, "message_count")
	}
	if got := h.memRead(outPtr, uint32(n)); string(got) != string(want) {
		t.Errorf("output buffer = %q, want %q", got, want)
	}
}

func TestSessionBridge_Forwarding_SessionFork(t *testing.T) {
	br := &recordingSessionBridge{forkResult: "session-9001"}
	h := newBridgeHarness(t).
		withCaps("session:fork").
		withSessionBridge(br).
		install()

	atRef := []byte("refs/sessions/abc/turns/3")
	seed := []byte("seed user message")
	h.memWrite(0, atRef)
	h.memWrite(64, seed)
	const outPtr, outCap = 256, 64

	n := h.callImport(context.Background(), "stado_session_fork",
		0, uint64(len(atRef)),
		64, uint64(len(seed)),
		outPtr, outCap)
	if n != int32(len("session-9001")) {
		t.Fatalf("got n=%d, want %d", n, len("session-9001"))
	}
	if br.lastForkAt != string(atRef) {
		t.Errorf("forwarded atTurn = %q, want %q", br.lastForkAt, atRef)
	}
	if br.lastForkSeed != string(seed) {
		t.Errorf("forwarded seed = %q, want %q", br.lastForkSeed, seed)
	}
	if got := h.memRead(outPtr, uint32(n)); string(got) != "session-9001" {
		t.Errorf("output buffer = %q, want session-9001", got)
	}
}

func TestSessionBridge_Forwarding_SessionNextEvent(t *testing.T) {
	want := []byte(`{"kind":"turn-start"}`)
	br := &recordingSessionBridge{eventResult: want}
	h := newBridgeHarness(t).
		withCaps("session:observe").
		withSessionBridge(br).
		install()

	const outPtr, outCap = 0, 256
	n := h.callImport(context.Background(), "stado_session_next_event",
		outPtr, outCap)
	if n != int32(len(want)) {
		t.Fatalf("got n=%d, want %d", n, len(want))
	}
	if got := h.memRead(outPtr, uint32(n)); string(got) != string(want) {
		t.Errorf("event buffer = %q, want %q", got, want)
	}
}

func TestSessionBridge_Forwarding_LLMInvoke(t *testing.T) {
	br := &recordingSessionBridge{llmReply: "hi from llm", llmTokens: 7}
	h := newBridgeHarness(t).
		withCaps("llm:invoke").
		withSessionBridge(br).
		install()

	args := llmInvokeArgs{Prompt: "say hi", Model: "claude-x", MaxTokens: 64}
	argBytes, _ := json.Marshal(args)
	h.memWrite(0, argBytes)
	const outPtr, outCap = 256, 256

	n := h.callImport(context.Background(), "stado_llm_invoke",
		0, uint64(len(argBytes)),
		outPtr, outCap)
	if n <= 0 {
		t.Fatalf("expected positive bytes-written, got %d", n)
	}
	if br.llmCalls.Load() != 1 {
		t.Errorf("llmCalls = %d, want 1", br.llmCalls.Load())
	}
	if br.lastLLMPrompt != "say hi" {
		t.Errorf("forwarded prompt = %q, want %q", br.lastLLMPrompt, "say hi")
	}
	if br.lastLLMOpts.Model != "claude-x" {
		t.Errorf("forwarded model = %q, want %q", br.lastLLMOpts.Model, "claude-x")
	}
	if br.lastLLMOpts.MaxTokens != 64 {
		t.Errorf("forwarded max_tokens = %d, want 64", br.lastLLMOpts.MaxTokens)
	}
	// stado_llm_invoke writes the bare reply string back (token
	// count is tracked internally on host.llmTokensUsed; not
	// part of the wire payload). Assert the reply and the host's
	// running budget total.
	if got := h.memRead(outPtr, uint32(n)); string(got) != "hi from llm" {
		t.Errorf("reply buffer = %q, want %q", got, "hi from llm")
	}
	if h.host.llmTokensUsed != 7 {
		t.Errorf("host.llmTokensUsed = %d, want 7 (bridge-reported)", h.host.llmTokensUsed)
	}
}

// ---- Contract 4: cancel propagation ------------------------------------
//
// Where the bridge method takes ctx, the host import must pass its
// own ctx down to the bridge, and a cancellation must reach the
// bridge's blocking call.
//
// Note on assertion shape: wazero's runtime is created with
// `WithCloseOnContextDone(true)` (see runtime.New), so cancelling
// the ctx passed to fn.Call() races with the closure's return —
// the module may be torn down before the closure writes its result
// to the stack. The contract under test here is that *the bridge
// sees the cancellation*, not what the closure's return value is.
// The tests assert via the recorder's captured ctx state.

func TestSessionBridge_Cancel_PropagatesToNextEvent(t *testing.T) {
	br := &recordingSessionBridge{blockEvent: true}
	h := newBridgeHarness(t).
		withCaps("session:observe").
		withSessionBridge(br).
		install()

	// Short timeout — the bridge will block on ctx.Done(), then
	// unblock when the deadline trips.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Don't go through the harness's t.Fatalf path — the import may
	// return "module closed" after the runtime tears the wasm
	// module down on ctx-done, which is implementation detail of
	// wazero, not the contract we're asserting.
	fn := h.thunkMod.ExportedFunction("thunk_stado_session_next_event")
	_, _ = fn.Call(ctx, 0, 256)

	if br.eventCalls.Load() != 1 {
		t.Errorf("bridge eventCalls = %d, want 1 (bridge must be reached)", br.eventCalls.Load())
	}
	if br.lastEventCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastEventCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastEventCtx.Err(), context.Canceled) {
		t.Errorf("bridge ctx not cancelled: %v", br.lastEventCtx.Err())
	}
}

func TestSessionBridge_Cancel_PropagatesToFork(t *testing.T) {
	br := &recordingSessionBridge{blockFork: true}
	h := newBridgeHarness(t).
		withCaps("session:fork").
		withSessionBridge(br).
		install()

	atRef := []byte("refs/sessions/x/turns/1")
	seed := []byte("seed")
	h.memWrite(0, atRef)
	h.memWrite(64, seed)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_session_fork")
	_, _ = fn.Call(ctx,
		0, uint64(len(atRef)),
		64, uint64(len(seed)),
		256, 64)

	if br.forkCalls.Load() != 1 {
		t.Errorf("bridge forkCalls = %d, want 1", br.forkCalls.Load())
	}
	if br.lastForkCtx == nil {
		t.Fatal("bridge never recorded a fork ctx")
	}
	if !errors.Is(br.lastForkCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastForkCtx.Err(), context.Canceled) {
		t.Errorf("bridge fork ctx not cancelled: %v", br.lastForkCtx.Err())
	}
}

func TestSessionBridge_Cancel_PropagatesToLLMInvoke(t *testing.T) {
	br := &recordingSessionBridge{blockLLM: true}
	h := newBridgeHarness(t).
		withCaps("llm:invoke").
		withSessionBridge(br).
		install()

	args := llmInvokeArgs{Prompt: "blocked"}
	argBytes, _ := json.Marshal(args)
	h.memWrite(0, argBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_llm_invoke")
	_, _ = fn.Call(ctx, 0, uint64(len(argBytes)), 256, 256)

	if br.llmCalls.Load() != 1 {
		t.Errorf("bridge llmCalls = %d, want 1", br.llmCalls.Load())
	}
	if br.lastLLMCtx == nil {
		t.Fatal("bridge never recorded an llm ctx")
	}
	if !errors.Is(br.lastLLMCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastLLMCtx.Err(), context.Canceled) {
		t.Errorf("bridge llm ctx not cancelled: %v", br.lastLLMCtx.Err())
	}
}
