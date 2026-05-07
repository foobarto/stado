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

// recordingFleetBridge records calls and returns programmable
// outcomes for every FleetBridge method.
type recordingFleetBridge struct {
	mu sync.Mutex

	spawnCalls   atomic.Int32
	listCalls    atomic.Int32
	readCalls    atomic.Int32
	sendCalls    atomic.Int32
	cancelCalls  atomic.Int32

	lastSpawnReq      AgentSpawnRequest
	lastReadID        string
	lastReadSince     int
	lastReadTimeoutMs int
	lastSendID        string
	lastSendMsg       string
	lastCancelID      string

	lastSpawnCtx  context.Context
	lastListCtx   context.Context
	lastReadCtx   context.Context
	lastSendCtx   context.Context
	lastCancelCtx context.Context

	spawnResult AgentSpawnResult
	listResult  []AgentListEntry
	readResult  AgentMessages

	spawnErr  error
	listErr   error
	readErr   error
	sendErr   error
	cancelErr error

	blockSpawn  bool
	blockList   bool
	blockRead   bool
	blockSend   bool
	blockCancel bool
}

func (b *recordingFleetBridge) AgentSpawn(ctx context.Context, req AgentSpawnRequest) (AgentSpawnResult, error) {
	b.spawnCalls.Add(1)
	b.mu.Lock()
	b.lastSpawnCtx = ctx
	b.lastSpawnReq = req
	block := b.blockSpawn
	res := b.spawnResult
	err := b.spawnErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return AgentSpawnResult{}, ctx.Err()
	}
	return res, err
}

func (b *recordingFleetBridge) AgentList(ctx context.Context) ([]AgentListEntry, error) {
	b.listCalls.Add(1)
	b.mu.Lock()
	b.lastListCtx = ctx
	block := b.blockList
	res := b.listResult
	err := b.listErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return res, err
}

func (b *recordingFleetBridge) AgentReadMessages(ctx context.Context, id string, since, timeoutMs int) (AgentMessages, error) {
	b.readCalls.Add(1)
	b.mu.Lock()
	b.lastReadCtx = ctx
	b.lastReadID = id
	b.lastReadSince = since
	b.lastReadTimeoutMs = timeoutMs
	block := b.blockRead
	res := b.readResult
	err := b.readErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return AgentMessages{}, ctx.Err()
	}
	return res, err
}

func (b *recordingFleetBridge) AgentSendMessage(ctx context.Context, id, msg string) error {
	b.sendCalls.Add(1)
	b.mu.Lock()
	b.lastSendCtx = ctx
	b.lastSendID = id
	b.lastSendMsg = msg
	block := b.blockSend
	err := b.sendErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (b *recordingFleetBridge) AgentCancel(ctx context.Context, id string) error {
	b.cancelCalls.Add(1)
	b.mu.Lock()
	b.lastCancelCtx = ctx
	b.lastCancelID = id
	block := b.blockCancel
	err := b.cancelErr
	b.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

// ---- Contract 1: capability gate ---------------------------------------

func TestFleetBridge_CapGate_DeniesEveryImportWithoutCap(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withFleetBridge(br).
		install()

	cases := []struct {
		name string
		args []uint64
	}{
		{"stado_agent_spawn", []uint64{0, 0, 0, 0}},
		{"stado_agent_list", []uint64{0, 0}},
		{"stado_agent_read_messages", []uint64{0, 0, 0, 0}},
		{"stado_agent_send_message", []uint64{0, 0}},
		{"stado_agent_cancel", []uint64{0, 0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.callImport(context.Background(), c.name, c.args...)
			if got != -1 {
				t.Errorf("%s with no cap: got %d, want -1", c.name, got)
			}
		})
	}
	total := br.spawnCalls.Load() + br.listCalls.Load() + br.readCalls.Load() +
		br.sendCalls.Load() + br.cancelCalls.Load()
	if total != 0 {
		t.Errorf("bridge invoked while caps denied: counters total=%d", total)
	}
}

// ---- Contract 2: nil-bridge --------------------------------------------

func TestFleetBridge_NilBridge_AllImportsReturnSentinel(t *testing.T) {
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		install()

	cases := []struct {
		name string
		args []uint64
	}{
		{"stado_agent_spawn", []uint64{0, 0, 0, 0}},
		{"stado_agent_list", []uint64{0, 0}},
		{"stado_agent_read_messages", []uint64{0, 0, 0, 0}},
		{"stado_agent_send_message", []uint64{0, 0}},
		{"stado_agent_cancel", []uint64{0, 0, 0, 0}},
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

func TestFleetBridge_Forwarding_Spawn(t *testing.T) {
	want := AgentSpawnResult{ID: "agent-7", SessionID: "sess-7", Status: "queued"}
	br := &recordingFleetBridge{spawnResult: want}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	req := AgentSpawnRequest{
		Prompt: "do the thing",
		Model:  "claude-x",
		Async:  true,
	}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)
	const resPtr, resCap = 256, 512

	n := h.callImport(context.Background(), "stado_agent_spawn",
		0, uint64(len(reqBytes)),
		resPtr, resCap)
	if n <= 0 {
		t.Fatalf("expected positive bytes-written, got %d", n)
	}
	if br.lastSpawnReq.Prompt != "do the thing" {
		t.Errorf("forwarded prompt = %q", br.lastSpawnReq.Prompt)
	}
	if br.lastSpawnReq.Model != "claude-x" {
		t.Errorf("forwarded model = %q", br.lastSpawnReq.Model)
	}
	if !br.lastSpawnReq.Async {
		t.Errorf("forwarded Async = false; want true")
	}
	out := h.memRead(resPtr, uint32(n))
	var got AgentSpawnResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode result: %v (raw=%s)", err, out)
	}
	if got.ID != want.ID || got.SessionID != want.SessionID || got.Status != want.Status {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestFleetBridge_Forwarding_List(t *testing.T) {
	want := []AgentListEntry{
		{ID: "a-1", SessionID: "s-1", Status: "running", Model: "m-x"},
		{ID: "a-2", SessionID: "s-2", Status: "done", Model: "m-y"},
	}
	br := &recordingFleetBridge{listResult: want}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	const resPtr, resCap = 0, 1024
	n := h.callImport(context.Background(), "stado_agent_list", resPtr, resCap)
	if n <= 0 {
		t.Fatalf("expected positive, got %d", n)
	}
	out := h.memRead(resPtr, uint32(n))
	var got []AgentListEntry
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode list: %v (raw=%s)", err, out)
	}
	if len(got) != 2 || got[0].ID != "a-1" || got[1].ID != "a-2" {
		t.Errorf("list = %+v, want a-1,a-2", got)
	}
}

func TestFleetBridge_Forwarding_ReadMessages(t *testing.T) {
	want := AgentMessages{
		Messages: []AgentMessage{
			{Role: "assistant", Content: "ok", Offset: 1},
		},
		Offset: 2,
		Status: "running",
	}
	br := &recordingFleetBridge{readResult: want}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	req := struct {
		ID        string `json:"id"`
		Since     int    `json:"since"`
		TimeoutMs int    `json:"timeout_ms"`
	}{ID: "agent-7", Since: 0, TimeoutMs: 100}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)
	const resPtr, resCap = 256, 1024

	n := h.callImport(context.Background(), "stado_agent_read_messages",
		0, uint64(len(reqBytes)),
		resPtr, resCap)
	if n <= 0 {
		t.Fatalf("expected positive, got %d", n)
	}
	if br.lastReadID != "agent-7" {
		t.Errorf("forwarded id = %q, want agent-7", br.lastReadID)
	}
	if br.lastReadTimeoutMs != 100 {
		t.Errorf("forwarded timeout = %d, want 100", br.lastReadTimeoutMs)
	}
	out := h.memRead(resPtr, uint32(n))
	var got AgentMessages
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if got.Status != "running" || got.Offset != 2 || len(got.Messages) != 1 {
		t.Errorf("messages = %+v, want %+v", got, want)
	}
}

func TestFleetBridge_Forwarding_SendMessage(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	req := struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}{ID: "agent-7", Message: "hello agent"}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)

	got := h.callImport(context.Background(), "stado_agent_send_message",
		0, uint64(len(reqBytes)))
	if got != 0 {
		t.Errorf("expected 0 on success, got %d", got)
	}
	if br.lastSendID != "agent-7" {
		t.Errorf("forwarded id = %q, want agent-7", br.lastSendID)
	}
	if br.lastSendMsg != "hello agent" {
		t.Errorf("forwarded msg = %q, want %q", br.lastSendMsg, "hello agent")
	}
}

func TestFleetBridge_Forwarding_Cancel(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	req := struct {
		ID string `json:"id"`
	}{ID: "agent-7"}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)
	const resPtr, resCap = 256, 256

	n := h.callImport(context.Background(), "stado_agent_cancel",
		0, uint64(len(reqBytes)),
		resPtr, resCap)
	if n <= 0 {
		t.Fatalf("expected positive, got %d", n)
	}
	if br.lastCancelID != "agent-7" {
		t.Errorf("forwarded id = %q, want agent-7", br.lastCancelID)
	}
	// Cancel returns {"ok": true}.
	out := h.memRead(resPtr, uint32(n))
	var resp map[string]bool
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode resp: %v (raw=%s)", err, out)
	}
	if !resp["ok"] {
		t.Errorf("response = %v, want {ok:true}", resp)
	}
}

// ---- Contract 4: cancel propagation ------------------------------------

func TestFleetBridge_Cancel_PropagatesToSpawn(t *testing.T) {
	br := &recordingFleetBridge{blockSpawn: true}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	req := AgentSpawnRequest{Prompt: "blocked"}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_agent_spawn")
	_, _ = fn.Call(ctx, 0, uint64(len(reqBytes)), 256, 256)

	if br.spawnCalls.Load() != 1 {
		t.Errorf("spawnCalls = %d, want 1", br.spawnCalls.Load())
	}
	if br.lastSpawnCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastSpawnCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastSpawnCtx.Err(), context.Canceled) {
		t.Errorf("ctx not cancelled: %v", br.lastSpawnCtx.Err())
	}
}

func TestFleetBridge_Cancel_PropagatesToReadMessages(t *testing.T) {
	br := &recordingFleetBridge{blockRead: true}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()

	req := struct {
		ID string `json:"id"`
	}{ID: "agent-7"}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_agent_read_messages")
	_, _ = fn.Call(ctx, 0, uint64(len(reqBytes)), 256, 1024)

	if br.readCalls.Load() != 1 {
		t.Errorf("readCalls = %d, want 1", br.readCalls.Load())
	}
	if br.lastReadCtx == nil {
		t.Fatal("bridge never recorded a ctx")
	}
	if !errors.Is(br.lastReadCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastReadCtx.Err(), context.Canceled) {
		t.Errorf("ctx not cancelled: %v", br.lastReadCtx.Err())
	}
}
