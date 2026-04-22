package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// TestHook_PostTurnFires: configuring a post_turn hook and then
// calling firePostTurnHook directly causes /bin/sh to run with the
// expected JSON payload on stdin. We don't drive a full turn loop
// here — firePostTurnHook is the integration point we care about.
func TestHook_PostTurnFires(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "model", "prov",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())

	out := filepath.Join(t.TempDir(), "hook.json")
	m.SetHooks("cat > " + out)
	// Populate the numbers the hook payload carries so the assertion
	// is meaningful.
	m.usage.InputTokens = 1234
	m.usage.OutputTokens = 567
	m.usage.CostUSD = 0.0125
	m.turnText = "hello world " + strings.Repeat("x", 300)
	m.turnStart = time.Now().Add(-50 * time.Millisecond)
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hi")}

	m.firePostTurnHook()

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook didn't write stdin: %v", err)
	}
	var p hooks.PostTurnPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("hook stdin wasn't JSON: %v\n%s", err, body)
	}
	if p.Event != "post_turn" {
		t.Errorf("event: got %q", p.Event)
	}
	if p.TokensIn != 1234 || p.TokensOut != 567 {
		t.Errorf("tokens lost: %+v", p)
	}
	if p.CostUSD != 0.0125 {
		t.Errorf("cost lost: %+v", p)
	}
	if len(p.TextExcerpt) > 200 {
		t.Errorf("excerpt not capped at 200 chars: %d", len(p.TextExcerpt))
	}
	if p.DurationMS < 10 {
		t.Errorf("duration suspiciously small: %d", p.DurationMS)
	}
}

// TestHook_PostTurnUnconfiguredIsNoop: with no hook configured,
// firePostTurnHook must do nothing — no /bin/sh invocation, no
// latency, no stderr noise.
func TestHook_PostTurnUnconfiguredIsNoop(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "model", "prov",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())

	start := time.Now()
	m.firePostTurnHook() // no hook set
	if time.Since(start) > 10*time.Millisecond {
		t.Errorf("no-op hook took unexpectedly long: %v", time.Since(start))
	}
}

func TestHook_PostTurnDisabledIsNoop(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(t.TempDir(), "model", "prov",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())

	out := filepath.Join(t.TempDir(), "hook.json")
	m.SetHooks("cat > " + out)
	m.hookRunner.Disabled = true
	m.firePostTurnHook()
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("disabled hook should not run, got %v", err)
	}
}
