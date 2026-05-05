package tui

import (
	"context"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rt "github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/subagent"
)

// lastSystemBlockBody returns the body of the most recently-appended
// system block, or "" if none exists. Test-only helper.
func (m *Model) lastSystemBlockBody() string {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "system" {
			return m.blocks[i].body
		}
	}
	return ""
}

// fakeSpawner mirrors internal/runtime/fleet_test.go's fakeSpawner —
// inlined here since it's package-private to the runtime package.
type fakeSpawner struct {
	calls atomic.Int32
	res   subagent.Result
	delay time.Duration
}

func (f *fakeSpawner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return subagent.Result{}, ctx.Err()
		}
	}
	return f.res, nil
}

// TestRenderPS_UsesTypedPrefix confirms /ps output uses the
// typed-prefix format ("agent:...") rather than bare ID strings.
func TestRenderPS_UsesTypedPrefix(t *testing.T) {
	fleet := rt.NewFleet()
	sp := &fakeSpawner{
		res:   subagent.Result{ChildSession: "child-1", Text: "done"},
		delay: 200 * time.Millisecond, // keep entry in 'running' state during render
	}
	id, err := fleet.Spawn(context.Background(), sp, "test-prompt", rt.SpawnOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = fleet.Cancel(id) })

	m := &Model{fleet: fleet}
	out := m.renderPS(false)

	// The agent line must match the typed-prefix shape:
	//   agent:<1-8 hex chars>  <status>  <model>  <duration> ago
	// Duration is `\S+` since time.Since(...) formatted output varies
	// (0s, 1ms, 100µs, etc.); the literal `ago` suffix is what we pin.
	agentLineRE := regexp.MustCompile(`^agent:[a-f0-9]{1,8}\s+running\s+test-model\s+\S+\s+ago$`)
	matched := false
	for _, line := range strings.Split(out, "\n") {
		if agentLineRE.MatchString(line) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("renderPS output should contain a typed-prefix agent line; got:\n%s", out)
	}

	// The full FleetID should not appear in the rendered line —
	// FormatFreeStandingHandleID trims it to 8 chars.
	if strings.Contains(out, id) && len(id) > 8 {
		t.Errorf("renderPS leaked full FleetID %q; should be trimmed", id)
	}
}

// TestHandleKillSlash_AcceptsTypedPrefix verifies /kill accepts
// both "agent:<id>" and bare "<id>" forms.
func TestHandleKillSlash_AcceptsTypedPrefix(t *testing.T) {
	fleet := rt.NewFleet()
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "done"},
		delay: 500 * time.Millisecond,
	}
	id, err := fleet.Spawn(context.Background(), sp, "p1", rt.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = fleet.Cancel(id) })

	// Form 1: typed prefix using the full id.
	m := &Model{fleet: fleet}
	m.handleKillSlash([]string{"/kill", "agent:" + id})

	// Verify Cancel landed.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		e, ok := fleet.Get(id)
		if ok && e.Status == rt.FleetStatusCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e, _ := fleet.Get(id); e.Status != rt.FleetStatusCancelled {
		t.Errorf("expected status cancelled; got %s", e.Status)
	}
}

// TestHandleKillSlash_BareIDStillWorks: back-compat — bare ID
// (no prefix) still routes to Fleet.Cancel.
func TestHandleKillSlash_BareIDStillWorks(t *testing.T) {
	fleet := rt.NewFleet()
	sp := &fakeSpawner{
		res:   subagent.Result{Text: "done"},
		delay: 500 * time.Millisecond,
	}
	id, err := fleet.Spawn(context.Background(), sp, "p2", rt.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = fleet.Cancel(id) })

	m := &Model{fleet: fleet}
	m.handleKillSlash([]string{"/kill", id})

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		e, ok := fleet.Get(id)
		if ok && e.Status == rt.FleetStatusCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e, _ := fleet.Get(id); e.Status != rt.FleetStatusCancelled {
		t.Errorf("expected status cancelled; got %s", e.Status)
	}
}

// TestHandleKillSlash_NonAgentTypeRejected: kill of non-agent
// typed handle (e.g. "proc:fs.7a2b") should report friendly error,
// not attempt a fleet cancel.
func TestHandleKillSlash_NonAgentTypeRejected(t *testing.T) {
	fleet := rt.NewFleet()
	m := &Model{fleet: fleet}
	m.handleKillSlash([]string{"/kill", "proc:fs.7a2b"})

	// The rejection message must have been appended as a system block.
	body := m.lastSystemBlockBody()
	if !strings.Contains(body, "only supports agent") {
		t.Errorf("expected rejection message mentioning 'only supports agent'; got: %q", body)
	}
	// And — fleet untouched (no entry was ever spawned, but make
	// the no-side-effect assertion explicit).
	if got := len(fleet.List()); got != 0 {
		t.Errorf("fleet shouldn't have entries after no-op kill; got %d", got)
	}
}
