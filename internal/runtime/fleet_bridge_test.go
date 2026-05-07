package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/subagent"
)

// Phase 1.3 of the 2026-Q2 refactor program: contract tests for
// internal/runtime/fleet_bridge.go (FleetBridgeAdapter), which
// adapts a *Fleet + Spawner into the pluginRuntime.FleetBridge
// interface consumed by the bundled agent plugin's stado_agent_*
// host imports.
//
// fleet_test.go covers Fleet directly (21 tests). fleet_bridge.go
// has been untested until now — that's the gap this file closes.
//
// Reuses the package-internal fakeSpawner + waitForStatus helpers
// from fleet_test.go (same package).

// newAdapterWithSpawner returns a FleetBridgeAdapter wired to a
// fresh Fleet and the given Spawner; ready for AgentSpawn et al.
func newAdapterWithSpawner(t *testing.T, sp Spawner) *FleetBridgeAdapter {
	t.Helper()
	return &FleetBridgeAdapter{
		Fleet:   NewFleet(),
		Spawner: sp,
		RootCtx: t.Context(),
	}
}

// ---- AgentSpawn ---------------------------------------------------------

func TestFleetBridgeAdapter_Spawn_EmptyPromptRejectedBeforeFleet(t *testing.T) {
	sp := &fakeSpawner{}
	a := newAdapterWithSpawner(t, sp)

	_, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{Prompt: ""})
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("expected 'prompt is required' error, got %v", err)
	}
	if sp.calls.Load() != 0 {
		t.Errorf("Spawner invoked despite missing prompt; calls=%d", sp.calls.Load())
	}
	if len(a.Fleet.List()) != 0 {
		t.Errorf("fleet entry created despite missing prompt: %+v", a.Fleet.List())
	}
}

func TestFleetBridgeAdapter_Spawn_AsyncReturnsRunningImmediately(t *testing.T) {
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "wouldn't reach this"},
		delay: 5 * time.Second, // long enough we won't wait for it
	}
	a := newAdapterWithSpawner(t, sp)

	start := time.Now()
	result, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "investigate",
		Async:  true,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AgentSpawn(async): %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("async spawn returned too slowly: %v (should be near-immediate)", elapsed)
	}
	if result.ID == "" {
		t.Error("async spawn returned empty ID")
	}
	if result.Status != string(FleetStatusRunning) {
		t.Errorf("async spawn status = %q, want %q", result.Status, FleetStatusRunning)
	}
	if result.FinalText != "" {
		t.Errorf("async spawn FinalText = %q, want empty", result.FinalText)
	}
}

func TestFleetBridgeAdapter_Spawn_SyncReturnsFinalTextOnCompletion(t *testing.T) {
	sp := &fakeSpawner{
		res:   subagent.Result{ChildSession: "child-7", Text: "found it"},
		delay: 50 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	result, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "find the bug",
		Async:  false,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}
	if result.Status != string(FleetStatusCompleted) {
		t.Errorf("status = %q, want %q", result.Status, FleetStatusCompleted)
	}
	if result.FinalText != "found it" {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "found it")
	}
	if result.SessionID != "child-7" {
		t.Errorf("SessionID = %q, want %q", result.SessionID, "child-7")
	}
}

func TestFleetBridgeAdapter_Spawn_SyncSurfacesSpawnerError(t *testing.T) {
	sp := &fakeSpawner{
		err:   errors.New("provider down"),
		delay: 50 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	_, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  false,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "agent error") || !strings.Contains(err.Error(), "provider down") {
		t.Errorf("error = %q, want both 'agent error' and 'provider down'", err.Error())
	}
}

func TestFleetBridgeAdapter_Spawn_SyncCtxCancelReturnsCtxErr(t *testing.T) {
	sp := &fakeSpawner{
		// Block long enough for our cancel to win.
		delay: 5 * time.Second,
	}
	a := newAdapterWithSpawner(t, sp)

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	_, err := a.AgentSpawn(ctx, pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  false,
	})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want ctx error", err)
	}
}

// TestFleetBridgeAdapter_Spawn_SyncCancelLeavesEntryRunning
// asserts the *actual* behavior on sync-spawn cancellation: the
// adapter returns ctx.Err to the caller but the spawn goroutine
// keeps running because Fleet.Spawn was launched with the
// long-lived RootCtx (fleet.go:205), not the caller's ctx.
//
// The plan called for "runtime state cleaned (no orphan record)"
// — that misread the design. The spawn goroutine survives caller
// cancellation by intent: a plugin can fire-and-forget a sync
// spawn, return early, and the agent still completes. Documenting
// the actual contract here so a future refactor doesn't quietly
// regress it.
func TestFleetBridgeAdapter_Spawn_SyncCancelLeavesEntryRunning(t *testing.T) {
	sp := &fakeSpawner{delay: 5 * time.Second}
	a := newAdapterWithSpawner(t, sp)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := a.AgentSpawn(ctx, pluginRuntime.AgentSpawnRequest{
		Prompt: "long task",
		Async:  false,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	// Entry must still exist after caller bails, status still
	// running. List() should report it.
	entries, listErr := a.AgentList(t.Context())
	if listErr != nil {
		t.Fatalf("AgentList: %v", listErr)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries after sync cancel, want 1 (orphan-by-design)", len(entries))
	}
	if len(entries) == 1 && entries[0].Status != string(FleetStatusRunning) {
		t.Errorf("entry status after sync cancel = %q, want %q (goroutine still running)",
			entries[0].Status, FleetStatusRunning)
	}
}

// ---- AgentList ---------------------------------------------------------

func TestFleetBridgeAdapter_List_EmptyFleetReturnsEmptySlice(t *testing.T) {
	a := newAdapterWithSpawner(t, &fakeSpawner{})

	got, err := a.AgentList(t.Context())
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries from empty fleet, want 0", len(got))
	}
}

func TestFleetBridgeAdapter_List_MapsFleetEntries(t *testing.T) {
	sp := &fakeSpawner{
		res:   subagent.Result{ChildSession: "c-1", Text: "ok"},
		delay: 30 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "investigate",
		Model:  "claude-x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}
	// Wait for completion so status maps to "completed".
	waitForStatus(t, a.Fleet, id.ID, FleetStatusCompleted)

	got, err := a.AgentList(t.Context())
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].ID != id.ID {
		t.Errorf("ID = %q, want %q", got[0].ID, id.ID)
	}
	if got[0].SessionID != "c-1" {
		t.Errorf("SessionID = %q, want c-1", got[0].SessionID)
	}
	if got[0].Status != string(FleetStatusCompleted) {
		t.Errorf("Status = %q, want completed", got[0].Status)
	}
	if got[0].StartedAt == "" {
		t.Errorf("StartedAt is empty; want RFC3339 timestamp")
	}
}

// ---- AgentReadMessages -------------------------------------------------

func TestFleetBridgeAdapter_ReadMessages_UnknownIDReturnsTypedError(t *testing.T) {
	a := newAdapterWithSpawner(t, &fakeSpawner{})

	_, err := a.AgentReadMessages(t.Context(), "no-such-agent", 0, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestFleetBridgeAdapter_ReadMessages_CompletedSurfacesSingleMessage(t *testing.T) {
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "the answer is 42"},
		delay: 30 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}
	waitForStatus(t, a.Fleet, id.ID, FleetStatusCompleted)

	msgs, err := a.AgentReadMessages(t.Context(), id.ID, 0, 0)
	if err != nil {
		t.Fatalf("AgentReadMessages: %v", err)
	}
	if msgs.Status != string(FleetStatusCompleted) {
		t.Errorf("Status = %q, want completed", msgs.Status)
	}
	if len(msgs.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs.Messages))
	}
	if msgs.Messages[0].Role != "assistant" {
		t.Errorf("Role = %q, want assistant", msgs.Messages[0].Role)
	}
	if msgs.Messages[0].Content != "the answer is 42" {
		t.Errorf("Content = %q", msgs.Messages[0].Content)
	}
	if msgs.Offset != 1 {
		t.Errorf("Offset = %d, want 1 (since=0 + 1 message)", msgs.Offset)
	}
}

func TestFleetBridgeAdapter_ReadMessages_RunningWithoutTimeoutReturnsImmediately(t *testing.T) {
	sp := &fakeSpawner{delay: 5 * time.Second}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}

	start := time.Now()
	msgs, err := a.AgentReadMessages(t.Context(), id.ID, 0, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AgentReadMessages: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("read returned too slowly: %v (timeoutMs=0 should not poll)", elapsed)
	}
	if msgs.Status != string(FleetStatusRunning) {
		t.Errorf("Status = %q, want running", msgs.Status)
	}
	if len(msgs.Messages) != 0 {
		t.Errorf("got %d messages while running with no result yet, want 0", len(msgs.Messages))
	}
}

func TestFleetBridgeAdapter_ReadMessages_TimeoutReturnsWhenStatusChanges(t *testing.T) {
	// Spawner takes 200ms; reader polls with 1s timeout — should
	// observe completion before the timeout.
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "done"},
		delay: 200 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}

	start := time.Now()
	msgs, err := a.AgentReadMessages(t.Context(), id.ID, 0, 1000)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AgentReadMessages: %v", err)
	}
	if msgs.Status != string(FleetStatusCompleted) {
		t.Errorf("Status = %q, want completed (poll should observe transition)", msgs.Status)
	}
	if elapsed >= time.Second {
		t.Errorf("read took the full timeout (%v); poll didn't observe completion early", elapsed)
	}
}

// TestFleetBridgeAdapter_ReadMessages_OffsetAboveCurrent: the plan
// called for "offset > current → empty, not error". Current
// implementation just echoes `since` back as msgs.Offset and
// returns no messages for an agent without a result yet. This
// matches the plan's "empty, not error" intent for the running
// case. Documenting current behavior so a future change doesn't
// silently introduce an error path.
func TestFleetBridgeAdapter_ReadMessages_OffsetAboveCurrent(t *testing.T) {
	sp := &fakeSpawner{delay: 5 * time.Second}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}

	msgs, err := a.AgentReadMessages(t.Context(), id.ID, 999, 0)
	if err != nil {
		t.Fatalf("AgentReadMessages: %v", err)
	}
	if len(msgs.Messages) != 0 {
		t.Errorf("got %d messages with since>current, want 0", len(msgs.Messages))
	}
	if msgs.Offset != 999 {
		t.Errorf("Offset = %d, want 999 (since echoed when no result)", msgs.Offset)
	}
}

// TestFleetBridgeAdapter_ReadMessages_OffsetNegative: the plan
// called for "offset < 0 → defined error". Current implementation
// does NOT validate `since`; it just echoes the negative value
// through. Documents the gap; aligning would be a behavior change
// captured for follow-up.
func TestFleetBridgeAdapter_ReadMessages_OffsetNegative(t *testing.T) {
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "result"},
		delay: 30 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}
	waitForStatus(t, a.Fleet, id.ID, FleetStatusCompleted)

	msgs, err := a.AgentReadMessages(t.Context(), id.ID, -1, 0)
	if err != nil {
		t.Fatalf("expected no error today (gap from plan), got %v", err)
	}
	// Current behavior: -1 is echoed as message Offset; msgs.Offset
	// becomes since+1 = 0.
	if msgs.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (since=-1 + 1; current behavior, plan said error)", msgs.Offset)
	}
	if len(msgs.Messages) != 1 || msgs.Messages[0].Offset != -1 {
		t.Errorf("messages = %+v, want one with Offset=-1 (current behavior)", msgs.Messages)
	}
}

func TestFleetBridgeAdapter_ReadMessages_AssertsForwardedSince(t *testing.T) {
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "ok"},
		delay: 30 * time.Millisecond,
	}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}
	waitForStatus(t, a.Fleet, id.ID, FleetStatusCompleted)

	const wantSince = 42
	msgs, err := a.AgentReadMessages(t.Context(), id.ID, wantSince, 0)
	if err != nil {
		t.Fatalf("AgentReadMessages: %v", err)
	}
	if len(msgs.Messages) != 1 || msgs.Messages[0].Offset != wantSince {
		t.Errorf("message offset = %v, want %d (since forwarded into Message.Offset)",
			msgs.Messages, wantSince)
	}
	if msgs.Offset != wantSince+1 {
		t.Errorf("Offset = %d, want %d (since+1 when result present)", msgs.Offset, wantSince+1)
	}
}

func TestFleetBridgeAdapter_ReadMessages_CtxCancelDuringPollReturns(t *testing.T) {
	// Long-running spawner; long-poll timeout; cancel via ctx.
	sp := &fakeSpawner{delay: 5 * time.Second}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = a.AgentReadMessages(ctx, id.ID, 0, 5000)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AgentReadMessages returned error %v; expected nil-with-stale-status (ctx.Done bails the poll)", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("read didn't bail on ctx cancel; elapsed=%v", elapsed)
	}
}

// ---- AgentSendMessage --------------------------------------------------

func TestFleetBridgeAdapter_SendMessage_UnknownIDPropagatesError(t *testing.T) {
	a := newAdapterWithSpawner(t, &fakeSpawner{})

	err := a.AgentSendMessage(t.Context(), "no-such-agent", "hello")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFleetBridgeAdapter_SendMessage_QueuesIntoInbox(t *testing.T) {
	sp := &fakeSpawner{delay: 5 * time.Second}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}

	if err := a.AgentSendMessage(t.Context(), id.ID, "hi agent"); err != nil {
		t.Fatalf("AgentSendMessage: %v", err)
	}
	// Inbox is drained by Fleet.DrainInbox; assert the message
	// landed there.
	got := a.Fleet.DrainInbox(id.ID)
	if len(got) != 1 || got[0] != "hi agent" {
		t.Errorf("inbox = %v, want [hi agent]", got)
	}
}

// ---- AgentCancel -------------------------------------------------------

// TestFleetBridgeAdapter_Cancel_UnknownIDIsIdempotent documents
// that AgentCancel on an unknown ID is currently a no-op (nil
// error), inherited from Fleet.Cancel's documented idempotency
// (fleet.go:279-292).
//
// The plan's per-bridge "missing-agent paths return typed
// not-found error" specification is *not* satisfied for Cancel
// today — Fleet.SendMessage / Fleet.Get / AgentReadMessages all
// distinguish unknown IDs, but Cancel deliberately doesn't.
// Aligning would be a behaviour change (existing callers depend
// on idempotency); out of scope for the no-behaviour-change
// program. Captured for follow-up.
func TestFleetBridgeAdapter_Cancel_UnknownIDIsIdempotent(t *testing.T) {
	a := newAdapterWithSpawner(t, &fakeSpawner{})

	if err := a.AgentCancel(t.Context(), "no-such-agent"); err != nil {
		t.Errorf("Cancel on unknown ID = %v, want nil (idempotent contract)", err)
	}
}

func TestFleetBridgeAdapter_Cancel_TerminatesRunningEntry(t *testing.T) {
	sp := &fakeSpawner{delay: 5 * time.Second}
	a := newAdapterWithSpawner(t, sp)

	id, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
		Prompt: "x",
		Async:  true,
	})
	if err != nil {
		t.Fatalf("AgentSpawn: %v", err)
	}
	if err := a.AgentCancel(t.Context(), id.ID); err != nil {
		t.Fatalf("AgentCancel: %v", err)
	}
	// Cancel is async — wait for the goroutine to observe ctx.Done.
	waitForStatus(t, a.Fleet, id.ID, FleetStatusCancelled)
}

// ---- Concurrency -------------------------------------------------------

// concurrentSpawner is a race-safe spawner for the concurrency
// test. The package's existing fakeSpawner writes a non-atomic
// gotPrompt field that races under parallel Spawn dispatch (the
// rest of fleet_test.go calls it serially so this is a latent
// issue there, not a regression here).
type concurrentSpawner struct {
	calls atomic.Int32
	delay time.Duration
}

func (s *concurrentSpawner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return subagent.Result{}, ctx.Err()
		}
	}
	return subagent.Result{Text: "ok"}, nil
}

func TestFleetBridgeAdapter_Spawn_ConcurrentSpawnsRegisterCleanly(t *testing.T) {
	sp := &concurrentSpawner{delay: 50 * time.Millisecond}
	a := newAdapterWithSpawner(t, sp)

	const N = 20
	var wg sync.WaitGroup
	ids := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := a.AgentSpawn(t.Context(), pluginRuntime.AgentSpawnRequest{
				Prompt: "task",
				Async:  true,
			})
			ids[idx] = res.ID
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Errorf("spawn[%d]: %v", i, err)
			continue
		}
		if ids[i] == "" {
			t.Errorf("spawn[%d]: empty ID", i)
			continue
		}
		if seen[ids[i]] {
			t.Errorf("duplicate ID %q at index %d", ids[i], i)
		}
		seen[ids[i]] = true
	}
	// All entries should appear in List.
	got, err := a.AgentList(t.Context())
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(got) != N {
		t.Errorf("List returned %d entries after %d spawns", len(got), N)
	}
}
