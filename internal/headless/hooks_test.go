package headless

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/pkg/agent"
)

type hookPromptProvider struct {
	text  string
	usage agent.Usage
}

func (p hookPromptProvider) Name() string                     { return "hook-test" }
func (p hookPromptProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p hookPromptProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	go func() {
		defer close(ch)
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: p.text}
		ch <- agent.Event{Kind: agent.EvDone, Usage: &p.usage}
	}()
	return ch, nil
}

func TestSessionPrompt_FiresPostTurnHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hook.json")
	cfg := &config.Config{}
	cfg.Hooks.PostTurn = "cat > " + out
	srv := NewServer(cfg, hookPromptProvider{
		text: "reply " + strings.Repeat("x", 300),
		usage: agent.Usage{
			InputTokens:  12,
			OutputTokens: 34,
			CostUSD:      0.56,
		},
	})
	srv.conn = acp.NewConn(strings.NewReader(""), io.Discard)

	res, err := srv.sessionNew()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := res.(sessionNewResult).SessionID

	raw := json.RawMessage(`{"sessionId":"` + sessionID + `","prompt":"hi"}`)
	if _, err := srv.sessionPrompt(context.Background(), raw); err != nil {
		t.Fatalf("sessionPrompt: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		body, err := os.ReadFile(out)
		if err == nil {
			var got hooks.PostTurnPayload
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal hook payload: %v", err)
			}
			if got.Event != "post_turn" || got.TurnIndex != 1 {
				t.Fatalf("payload header: %+v", got)
			}
			if got.TokensIn != 12 || got.TokensOut != 34 || got.CostUSD != 0.56 {
				t.Fatalf("usage lost: %+v", got)
			}
			if len(got.TextExcerpt) != 200 {
				t.Fatalf("excerpt len = %d, want 200", len(got.TextExcerpt))
			}
			if got.DurationMS < 0 {
				t.Fatalf("duration should be non-negative: %+v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook output not written: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
