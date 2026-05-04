package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/subagent"
)

// fakeSpawner returns canned results without actually forking a
// session. tracks invocation count.
type fakeSpawner struct {
	calls   atomic.Int32
	res     subagent.Result
	err     error
	delay   time.Duration
	gotPrompt string
}

func (f *fakeSpawner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	f.calls.Add(1)
	f.gotPrompt = req.Prompt
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return subagent.Result{}, ctx.Err()
		}
	}
	if f.err != nil {
		return subagent.Result{}, f.err
	}
	return f.res, nil
}

// waitForStatus polls for an entry to reach the target status. Times
// out after 2s with t.Fatal.
func waitForStatus(t *testing.T, f *Fleet, id string, want FleetStatus) FleetEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e, ok := f.Get(id)
		if ok && e.Status == want {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for status %s on %s", want, id)
	return FleetEntry{}
}

func TestFleet_Spawn_RecordsRunningEntry(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{
		res:   subagent.Result{ChildSession: "child-1", Text: "done"},
		delay: 50 * time.Millisecond,
	}
	id, err := f.Spawn(context.Background(), sp, "investigate target X", SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	e, ok := f.Get(id)
	if !ok {
		t.Fatal("entry missing after Spawn returned")
	}
	if e.Status != FleetStatusRunning {
		t.Errorf("initial status = %s, want running", e.Status)
	}
	if e.Prompt != "investigate target X" {
		t.Errorf("prompt = %q", e.Prompt)
	}
}

func TestFleet_Spawn_TransitionsToCompleted(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{
		res:   subagent.Result{ChildSession: "child-1", Text: "all done"},
		delay: 30 * time.Millisecond,
	}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})

	e := waitForStatus(t, f, id, FleetStatusCompleted)
	if e.Result != "all done" {
		t.Errorf("Result = %q", e.Result)
	}
	if e.SessionID != "child-1" {
		t.Errorf("SessionID = %q, want child-1", e.SessionID)
	}
	if e.EndedAt.IsZero() {
		t.Error("EndedAt zero on terminal entry")
	}
}

func TestFleet_Spawn_TransitionsToErrorOnSpawnerFailure(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{err: errors.New("provider down")}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})

	e := waitForStatus(t, f, id, FleetStatusError)
	if !strings.Contains(e.Error, "provider down") {
		t.Errorf("Error = %q, expected 'provider down' substring", e.Error)
	}
}

func TestFleet_Cancel_RunningEntry_TransitionsToCancelled(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{delay: 5 * time.Second} // long delay; we cancel
	id, _ := f.Spawn(context.Background(), sp, "long task", SpawnOptions{})

	// Give the goroutine a moment to start, then cancel.
	time.Sleep(20 * time.Millisecond)
	if err := f.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	e := waitForStatus(t, f, id, FleetStatusCancelled)
	if e.EndedAt.IsZero() {
		t.Error("EndedAt zero on cancelled entry")
	}
}

func TestFleet_Cancel_TerminalEntry_NoOp(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{res: subagent.Result{ChildSession: "x", Text: "done"}}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})
	waitForStatus(t, f, id, FleetStatusCompleted)
	// Cancel after completion — should not error, status stays
	// completed.
	if err := f.Cancel(id); err != nil {
		t.Errorf("Cancel on terminal entry: %v", err)
	}
	e, _ := f.Get(id)
	if e.Status != FleetStatusCompleted {
		t.Errorf("status changed after Cancel-on-terminal: %s", e.Status)
	}
}

func TestFleet_List_OrdersRunningFirst(t *testing.T) {
	f := NewFleet()
	// One completed, one running, one cancelled.
	spDone := &fakeSpawner{res: subagent.Result{Text: "ok"}}
	spLong := &fakeSpawner{delay: 5 * time.Second}
	spDone2 := &fakeSpawner{res: subagent.Result{Text: "ok"}}

	idDone, _ := f.Spawn(context.Background(), spDone, "done-1", SpawnOptions{})
	waitForStatus(t, f, idDone, FleetStatusCompleted)

	idLong, _ := f.Spawn(context.Background(), spLong, "running", SpawnOptions{})
	time.Sleep(20 * time.Millisecond) // ensure it started

	idCancel, _ := f.Spawn(context.Background(), spDone2, "to-cancel", SpawnOptions{})
	waitForStatus(t, f, idCancel, FleetStatusCompleted)

	list := f.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	if list[0].Status != FleetStatusRunning {
		t.Errorf("expected running first, got %s (id=%s)", list[0].Status, list[0].FleetID)
	}
	// Cleanup so the long-running goroutine doesn't leak the test.
	_ = f.Cancel(idLong)
}

func TestFleet_UpdateProgress_TerminalEntry_NoOp(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{res: subagent.Result{Text: "ok"}}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})
	waitForStatus(t, f, id, FleetStatusCompleted)

	f.UpdateProgress(id, "bash", "should not stick")
	e, _ := f.Get(id)
	if e.LastTool == "bash" {
		t.Error("UpdateProgress mutated a terminal entry")
	}
}

func TestFleet_UpdateProgress_RunningEntry_BumpsLast(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{delay: 5 * time.Second}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})
	defer func() { _ = f.Cancel(id) }()

	time.Sleep(20 * time.Millisecond)
	f.UpdateProgress(id, "read", "scanning README.md")
	e, _ := f.Get(id)
	if e.LastTool != "read" {
		t.Errorf("LastTool = %q", e.LastTool)
	}
	if !strings.Contains(e.LastText, "scanning") {
		t.Errorf("LastText = %q", e.LastText)
	}
}

func TestFleet_Remove_RunningEntry_Refused(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{delay: 5 * time.Second}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})
	defer func() { _ = f.Cancel(id) }()
	time.Sleep(20 * time.Millisecond)

	if f.Remove(id) {
		t.Error("Remove should refuse running entries")
	}
}

func TestFleet_Remove_TerminalEntry_Removed(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{res: subagent.Result{Text: "ok"}}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})
	waitForStatus(t, f, id, FleetStatusCompleted)
	if !f.Remove(id) {
		t.Error("Remove failed on terminal entry")
	}
	if _, ok := f.Get(id); ok {
		t.Error("entry still present after Remove")
	}
}

func TestFleet_CancelAll_CancelsRunningEntries(t *testing.T) {
	f := NewFleet()
	sp1 := &fakeSpawner{delay: 5 * time.Second}
	sp2 := &fakeSpawner{delay: 5 * time.Second}
	id1, _ := f.Spawn(context.Background(), sp1, "a", SpawnOptions{})
	id2, _ := f.Spawn(context.Background(), sp2, "b", SpawnOptions{})
	time.Sleep(20 * time.Millisecond)

	f.CancelAll()
	waitForStatus(t, f, id1, FleetStatusCancelled)
	waitForStatus(t, f, id2, FleetStatusCancelled)
}

func TestFleet_FindByChildSession(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{res: subagent.Result{ChildSession: "ch-42", Text: "ok"}}
	id, _ := f.Spawn(context.Background(), sp, "p", SpawnOptions{})
	waitForStatus(t, f, id, FleetStatusCompleted)

	got, ok := f.FindByChildSession("ch-42")
	if !ok || got != id {
		t.Errorf("FindByChildSession = (%q, %v), want (%q, true)", got, ok, id)
	}
	if _, ok := f.FindByChildSession("missing"); ok {
		t.Error("expected miss for unknown session id")
	}
}

func TestFleetEntry_Summary_TruncatesPrompt(t *testing.T) {
	e := FleetEntry{
		FleetID:  "12345678abcd",
		Status:   FleetStatusRunning,
		Prompt:   strings.Repeat("a", 200),
		LastTool: "bash",
	}
	s := e.Summary()
	if !strings.HasPrefix(s, "12345678") {
		t.Errorf("summary should start with short id: %q", s)
	}
	if !strings.Contains(s, "running") {
		t.Errorf("summary missing status: %q", s)
	}
	if !strings.Contains(s, "last: bash") {
		t.Errorf("summary missing last tool: %q", s)
	}
}

func TestFleet_Spawn_RejectsEmptyPrompt(t *testing.T) {
	f := NewFleet()
	if _, err := f.Spawn(context.Background(), &fakeSpawner{}, "  ", SpawnOptions{}); err == nil {
		t.Error("expected error for empty prompt")
	}
}

func TestFleet_Spawn_RejectsNilSpawner(t *testing.T) {
	f := NewFleet()
	if _, err := f.Spawn(context.Background(), nil, "p", SpawnOptions{}); err == nil {
		t.Error("expected error for nil spawner")
	}
}

func TestFleet_RootCtxCancelled_CancelsEntry(t *testing.T) {
	f := NewFleet()
	sp := &fakeSpawner{delay: 5 * time.Second}
	rootCtx, cancel := context.WithCancel(context.Background())
	id, _ := f.Spawn(rootCtx, sp, "p", SpawnOptions{})
	time.Sleep(20 * time.Millisecond)

	cancel()
	waitForStatus(t, f, id, FleetStatusCancelled)
}
