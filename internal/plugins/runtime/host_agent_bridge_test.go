package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingFleetBridge records calls and returns programmable
// outcomes for every FleetBridge method.
type recordingFleetBridge struct {
	mu sync.Mutex

	spawnCalls  atomic.Int32
	listCalls   atomic.Int32
	readCalls   atomic.Int32
	sendCalls   atomic.Int32
	cancelCalls atomic.Int32

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

func TestFleetBridge_OperationScopedCapabilitiesDoNotWiden(t *testing.T) {
	br := &recordingFleetBridge{listResult: []AgentListEntry{{ID: "agent-1"}}}
	h := newBridgeHarness(t).
		withCaps("agent:list").
		withFleetBridge(br).
		install()
	if got := h.callImport(context.Background(), "stado_agent_list", 1024, 4096); got <= 0 {
		t.Fatalf("agent:list returned %d", got)
	}
	if got := h.callImport(context.Background(), "stado_agent_spawn", 0, 0, 1024, 4096); got != -1 {
		t.Fatalf("agent:list widened to spawn: got %d", got)
	}
	if br.listCalls.Load() != 1 || br.spawnCalls.Load() != 0 {
		t.Fatalf("bridge calls list=%d spawn=%d", br.listCalls.Load(), br.spawnCalls.Load())
	}
}

func TestFleetBridge_LegacyAggregateCapabilityGrantsNoOperation(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withCaps("agent:fleet").
		withFleetBridge(br).
		install()
	if got := h.callImport(context.Background(), "stado_agent_list", 1024, 4096); got != -1 {
		t.Fatalf("legacy aggregate capability returned %d, want denial", got)
	}
	if br.listCalls.Load() != 0 {
		t.Fatal("legacy aggregate capability reached fleet bridge")
	}
}

// ---- Contract 2: nil-bridge --------------------------------------------

func TestFleetBridge_NilBridge_AllImportsReturnSentinel(t *testing.T) {
	h := newBridgeHarness(t).
		withCaps("agent:spawn", "agent:list", "agent:read", "agent:send", "agent:cancel").
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
		withCaps("agent:spawn", "agent:spawn:configure").
		withFleetBridge(br).
		install()

	// Cover every AgentSpawnRequest field so forwarding regressions
	// in B3's bridge wiring don't slip past as silent zeroings.
	req := AgentSpawnRequest{
		Prompt:               "do the thing",
		Provider:             "anthropic",
		Model:                "claude-x",
		Thinking:             "on",
		ThinkingBudgetTokens: 8000,
		ReasoningEffort:      "xhigh",
		Async:                true,
		Ephemeral:            true,
		ParentSession:        "parent-sess-1",
		AllowedTools:         []string{"fs_read", "rg__rg"},
		SandboxProfile:       "strict",
		Persona:              "researcher",
		Role:                 "worker",
		Mode:                 "workspace_write",
		Ownership:            "implement the parser",
		WriteScope:           []string{"internal/parser/**"},
		MaxTurns:             9,
		TimeoutSeconds:       420,
		Source:               &AgentSource{SessionID: "source-1", At: "turns/3"},
		ToolProfile:          "worker-safe",
		NarrowTools:          []string{"fs__read", "fs__write"},
		TokenBudget:          12000,
		Execution:            "retained",
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
	got := br.lastSpawnReq
	if !reflect.DeepEqual(got, req) {
		t.Errorf("forwarded request = %#v, want %#v", got, req)
	}
	if got.Prompt != req.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, req.Prompt)
	}
	if got.Model != req.Model {
		t.Errorf("Model = %q, want %q", got.Model, req.Model)
	}
	if got.Provider != req.Provider || got.Thinking != req.Thinking || got.ThinkingBudgetTokens != req.ThinkingBudgetTokens || got.ReasoningEffort != req.ReasoningEffort {
		t.Errorf("provider profile = %#v, want %#v", got, req)
	}
	if got.Async != req.Async {
		t.Errorf("Async = %v, want %v", got.Async, req.Async)
	}
	if got.Ephemeral != req.Ephemeral {
		t.Errorf("Ephemeral = %v, want %v", got.Ephemeral, req.Ephemeral)
	}
	if got.ParentSession != req.ParentSession {
		t.Errorf("ParentSession = %q, want %q", got.ParentSession, req.ParentSession)
	}
	if len(got.AllowedTools) != len(req.AllowedTools) ||
		got.AllowedTools[0] != req.AllowedTools[0] ||
		got.AllowedTools[1] != req.AllowedTools[1] {
		t.Errorf("AllowedTools = %v, want %v", got.AllowedTools, req.AllowedTools)
	}
	if got.SandboxProfile != req.SandboxProfile {
		t.Errorf("SandboxProfile = %q, want %q", got.SandboxProfile, req.SandboxProfile)
	}
	if got.Persona != req.Persona {
		t.Errorf("Persona = %q, want %q", got.Persona, req.Persona)
	}
	out := h.memRead(resPtr, uint32(n))
	var resp AgentSpawnResult
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode result: %v (raw=%s)", err, out)
	}
	if resp.ID != want.ID || resp.SessionID != want.SessionID || resp.Status != want.Status {
		t.Errorf("result = %+v, want %+v", resp, want)
	}
}

func TestFleetBridge_SpawnIdempotencyScopeIsHostInjected(t *testing.T) {
	br := &recordingFleetBridge{spawnResult: AgentSpawnResult{ID: "agent-1", Status: "running"}}
	h := newBridgeHarness(t).
		withCaps("agent:spawn").
		withFleetBridge(br).
		withApplicationScope("broker-session-1", 7).
		install()
	reqBytes, _ := json.Marshal(AgentSpawnRequest{Prompt: "review", Async: true, IdempotencyKey: "review:turn-9"})
	h.memWrite(0, reqBytes)
	if got := h.callImport(context.Background(), "stado_agent_spawn", 0, uint64(len(reqBytes)), 1024, 4096); got <= 0 {
		t.Fatalf("spawn returned %d", got)
	}
	got := br.lastSpawnReq
	if got.IdempotencyKey != "review:turn-9" {
		t.Fatalf("idempotency key = %q", got.IdempotencyKey)
	}
	if got.Caller.PluginID != h.host.Identity.Namespace || got.Caller.SessionID != "broker-session-1" || got.Caller.Generation != 7 {
		t.Fatalf("caller scope = %#v", got.Caller)
	}
}

func TestFleetBridge_SpawnIdempotencyRequiresLifecycleScope(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withCaps("agent:spawn").
		withFleetBridge(br).
		install()
	reqBytes, _ := json.Marshal(AgentSpawnRequest{Prompt: "review", Async: true, IdempotencyKey: "review:turn-9"})
	h.memWrite(0, reqBytes)
	if got := h.callImport(context.Background(), "stado_agent_spawn", 0, uint64(len(reqBytes)), 1024, 4096); got != -1 {
		t.Fatalf("spawn returned %d, want denial", got)
	}
	if br.spawnCalls.Load() != 0 {
		t.Fatal("unscoped idempotent spawn reached fleet bridge")
	}
}

func TestFleetBridge_SpawnConfigurationRequiresSeparateSignedCapability(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withCaps("agent:spawn").
		withFleetBridge(br).
		install()
	reqBytes, _ := json.Marshal(AgentSpawnRequest{Prompt: "review", Provider: "anthropic", Model: "claude", Thinking: "on"})
	h.memWrite(0, reqBytes)
	if got := h.callImport(context.Background(), "stado_agent_spawn", 0, uint64(len(reqBytes)), 1024, 4096); got != -1 {
		t.Fatalf("configured spawn returned %d, want denial", got)
	}
	if br.spawnCalls.Load() != 0 {
		t.Fatal("configured spawn reached bridge without configure capability")
	}
}

func TestFleetBridge_Forwarding_List(t *testing.T) {
	want := []AgentListEntry{
		{ID: "a-1", SessionID: "s-1", Status: "running", Model: "m-x"},
		{ID: "a-2", SessionID: "s-2", Status: "done", Model: "m-y"},
	}
	br := &recordingFleetBridge{listResult: want}
	h := newBridgeHarness(t).
		withCaps("agent:list").
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
		Terminal: &AgentTerminalMetadata{
			Usage: AgentTokenUsage{InputTokens: 12, OutputTokens: 4}, UsageComplete: true,
			Cleanup: &AgentCleanupDiagnostic{Kind: "provider_close", Fingerprint: "sha256:abc"},
		},
	}
	br := &recordingFleetBridge{readResult: want}
	h := newBridgeHarness(t).
		withCaps("agent:read").
		withFleetBridge(br).
		install()

	// Non-zero Since to catch the case where the import drops it.
	req := struct {
		ID        string `json:"id"`
		Since     int    `json:"since"`
		TimeoutMs int    `json:"timeout_ms"`
	}{ID: "agent-7", Since: 5, TimeoutMs: 100}
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
	if br.lastReadSince != 5 {
		t.Errorf("forwarded since = %d, want 5", br.lastReadSince)
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
	if got.Terminal == nil || !got.Terminal.UsageComplete || got.Terminal.Usage.InputTokens != 12 ||
		got.Terminal.Cleanup == nil || got.Terminal.Cleanup.Fingerprint != "sha256:abc" {
		t.Fatalf("terminal metadata = %#v", got.Terminal)
	}
}

func TestFleetBridge_Forwarding_SendMessage(t *testing.T) {
	br := &recordingFleetBridge{}
	h := newBridgeHarness(t).
		withCaps("agent:send").
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
		withCaps("agent:cancel").
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
		withCaps("agent:spawn").
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
		withCaps("agent:read").
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

func TestFleetBridge_Cancel_PropagatesToList(t *testing.T) {
	br := &recordingFleetBridge{blockList: true}
	h := newBridgeHarness(t).
		withCaps("agent:list").
		withFleetBridge(br).
		install()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_agent_list")
	_, _ = fn.Call(ctx, 0, 1024)

	if br.listCalls.Load() != 1 {
		t.Errorf("listCalls = %d, want 1", br.listCalls.Load())
	}
	if br.lastListCtx == nil {
		t.Fatal("bridge never recorded a list ctx")
	}
	if !errors.Is(br.lastListCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastListCtx.Err(), context.Canceled) {
		t.Errorf("list ctx not cancelled: %v", br.lastListCtx.Err())
	}
}

func TestFleetBridge_Cancel_PropagatesToSendMessage(t *testing.T) {
	br := &recordingFleetBridge{blockSend: true}
	h := newBridgeHarness(t).
		withCaps("agent:send").
		withFleetBridge(br).
		install()

	req := struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}{ID: "agent-7", Message: "hi"}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_agent_send_message")
	_, _ = fn.Call(ctx, 0, uint64(len(reqBytes)))

	if br.sendCalls.Load() != 1 {
		t.Errorf("sendCalls = %d, want 1", br.sendCalls.Load())
	}
	if br.lastSendCtx == nil {
		t.Fatal("bridge never recorded a send ctx")
	}
	if !errors.Is(br.lastSendCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastSendCtx.Err(), context.Canceled) {
		t.Errorf("send ctx not cancelled: %v", br.lastSendCtx.Err())
	}
}

func TestFleetBridge_Cancel_PropagatesToCancel(t *testing.T) {
	br := &recordingFleetBridge{blockCancel: true}
	h := newBridgeHarness(t).
		withCaps("agent:cancel").
		withFleetBridge(br).
		install()

	req := struct {
		ID string `json:"id"`
	}{ID: "agent-7"}
	reqBytes, _ := json.Marshal(req)
	h.memWrite(0, reqBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fn := h.thunkMod.ExportedFunction("thunk_stado_agent_cancel")
	_, _ = fn.Call(ctx, 0, uint64(len(reqBytes)), 256, 256)

	if br.cancelCalls.Load() != 1 {
		t.Errorf("cancelCalls = %d, want 1", br.cancelCalls.Load())
	}
	if br.lastCancelCtx == nil {
		t.Fatal("bridge never recorded a cancel ctx")
	}
	if !errors.Is(br.lastCancelCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(br.lastCancelCtx.Err(), context.Canceled) {
		t.Errorf("cancel ctx not cancelled: %v", br.lastCancelCtx.Err())
	}
}
