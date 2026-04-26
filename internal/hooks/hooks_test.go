package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

// TestFirePostTurn_RunsCommandWithPayload: a configured post_turn
// hook runs /bin/sh -c <cmd> with the JSON payload piped to stdin.
// We redirect stdin to a temp file via the command, then read the
// file back and confirm it parses as PostTurnPayload.
func TestFirePostTurn_RunsCommandWithPayload(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stdin.json")
	r := &Runner{
		PostTurnCmd: "cat > " + out,
		Logger:      &bytes.Buffer{}, // suppress stderr noise
	}
	r.FirePostTurn(context.Background(), PostTurnPayload{
		Event:      "post_turn",
		TurnIndex:  3,
		TokensIn:   1000,
		TokensOut:  200,
		CostUSD:    0.05,
		DurationMS: 1234,
	})
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("stdin file not written: %v", err)
	}
	var got PostTurnPayload
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("stdin wasn't valid JSON: %v\nbody=%q", err, body)
	}
	if got.TurnIndex != 3 || got.TokensIn != 1000 || got.CostUSD != 0.05 {
		t.Errorf("payload lost fields: %+v", got)
	}
}

// TestFirePostTurn_EmptyCmdIsNoop: the zero value of Runner (or an
// explicit empty PostTurnCmd) silently does nothing. Callers can
// always fire without a nil check.
func TestFirePostTurn_EmptyCmdIsNoop(t *testing.T) {
	var r Runner
	// If this panics or hangs, the test will time out via `go test`.
	r.FirePostTurn(context.Background(), PostTurnPayload{})
	// And nil receiver form (zero-alloc path) is safe too.
	var rn *Runner
	rn.FirePostTurn(context.Background(), PostTurnPayload{})
}

// TestFirePostTurn_ErrorLogged_NotPropagated: a hook that exits
// non-zero has its failure logged to the Logger, but FirePostTurn
// returns nothing — the turn has already completed and a broken
// hook must not affect it.
func TestFirePostTurn_ErrorLogged_NotPropagated(t *testing.T) {
	var log bytes.Buffer
	r := &Runner{
		PostTurnCmd: "exit 42",
		Logger:      &log,
	}
	r.FirePostTurn(context.Background(), PostTurnPayload{Event: "post_turn"})
	if !strings.Contains(log.String(), "exit") && !strings.Contains(log.String(), "hook") {
		t.Errorf("expected failure to be logged; got %q", log.String())
	}
}

// TestFirePostTurn_Timeout: a hook that sleeps past the timeout is
// killed; the error surfaces in the log. Timeout is set tight so the
// test doesn't hang if the cap is broken.
func TestFirePostTurn_Timeout(t *testing.T) {
	var log bytes.Buffer
	r := &Runner{
		PostTurnCmd: "sleep 5",
		Logger:      &log,
		Timeout:     100 * time.Millisecond,
	}
	start := time.Now()
	r.FirePostTurn(context.Background(), PostTurnPayload{Event: "post_turn"})
	if time.Since(start) > time.Second {
		t.Errorf("hook ran past the timeout cap: %v", time.Since(start))
	}
	if !strings.Contains(log.String(), "hook") {
		t.Errorf("timeout error not logged: %q", log.String())
	}
}

func TestFirePostTurn_CapsHookOutput(t *testing.T) {
	var log bytes.Buffer
	r := &Runner{
		PostTurnCmd: "yes x | head -c 20000",
		Logger:      &log,
	}
	r.FirePostTurn(context.Background(), PostTurnPayload{Event: "post_turn"})
	got := log.String()
	if !strings.Contains(got, "truncated") {
		t.Fatalf("expected hook output truncation marker, got %q", got[max0(len(got)-200):])
	}
	if len(got) > maxHookOutputBytes+512 {
		t.Fatalf("hook log length = %d, want bounded", len(got))
	}
}

func TestNewPostTurnPayload_NormalizesFields(t *testing.T) {
	p := NewPostTurnPayload(7, agent.Usage{
		InputTokens:  123,
		OutputTokens: 45,
		CostUSD:      0.67,
	}, "hello "+strings.Repeat("x", 300), 1250*time.Millisecond)

	if p.Event != "post_turn" || p.TurnIndex != 7 {
		t.Fatalf("unexpected payload header: %+v", p)
	}
	if p.TokensIn != 123 || p.TokensOut != 45 || p.CostUSD != 0.67 {
		t.Fatalf("usage lost: %+v", p)
	}
	if len(p.TextExcerpt) != 200 {
		t.Fatalf("excerpt len = %d, want 200", len(p.TextExcerpt))
	}
	if p.DurationMS != 1250 {
		t.Fatalf("duration = %d, want 1250", p.DurationMS)
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func TestDisabledByToolConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "default", cfg: config.Config{}, want: false},
		{name: "enabled includes bash", cfg: config.Config{Tools: config.Tools{Enabled: []string{"read", "bash"}}}, want: false},
		{name: "enabled excludes bash", cfg: config.Config{Tools: config.Tools{Enabled: []string{"read"}}}, want: true},
		{name: "disabled removes bash", cfg: config.Config{Tools: config.Tools{Disabled: []string{"bash"}}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisabledByToolConfig(&tc.cfg); got != tc.want {
				t.Fatalf("DisabledByToolConfig() = %v, want %v", got, tc.want)
			}
		})
	}
}
