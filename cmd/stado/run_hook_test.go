package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

type runHookProvider struct{}

func (runHookProvider) Name() string                     { return "hook-test" }
func (runHookProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (runHookProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	panic("runHookProvider.StreamTurn should not be called directly in this test")
}

func TestRun_FiresPostTurnHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hook.json")

	oldLoadConfig := runLoadConfig
	oldBuildProvider := runBuildProvider
	oldAgentLoop := runAgentLoop
	oldPrompt, oldSkill, oldSessionID := runPrompt, runSkill, runSessionID
	oldMaxTurns, oldJSON, oldNoTools, oldSandboxFS := runMaxTurns, runJSON, runNoTools, runSandboxFS
	defer func() {
		runLoadConfig = oldLoadConfig
		runBuildProvider = oldBuildProvider
		runAgentLoop = oldAgentLoop
		runPrompt, runSkill, runSessionID = oldPrompt, oldSkill, oldSessionID
		runMaxTurns, runJSON, runNoTools, runSandboxFS = oldMaxTurns, oldJSON, oldNoTools, oldSandboxFS
	}()

	runLoadConfig = func() (*config.Config, error) {
		cfg := &config.Config{}
		cfg.Defaults.Model = "test-model"
		cfg.Hooks.PostTurn = "cat > " + out
		return cfg, nil
	}
	runBuildProvider = func(*config.Config) (agent.Provider, error) {
		return runHookProvider{}, nil
	}
	runAgentLoop = func(_ context.Context, opts runtime.AgentLoopOptions) (string, []agent.Message, error) {
		if opts.OnTurnComplete == nil {
			t.Fatal("OnTurnComplete not wired")
		}
		opts.OnTurnComplete(1, "reply "+strings.Repeat("x", 300), nil, agent.Usage{
			InputTokens:  12,
			OutputTokens: 34,
			CostUSD:      0.56,
		}, 75*time.Millisecond)
		return "done", opts.Messages, nil
	}

	runPrompt = "hi"
	runSkill = ""
	runSessionID = ""
	runMaxTurns = 1
	runJSON = true
	runNoTools = true
	runSandboxFS = false

	restore := chdir(t, t.TempDir())
	defer restore()

	runCmd.SetContext(context.Background())
	if err := runCmd.RunE(runCmd, nil); err != nil {
		t.Fatalf("runCmd.RunE: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read hook output: %v", err)
	}
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
	if got.DurationMS != 75 {
		t.Fatalf("duration = %d, want 75", got.DurationMS)
	}
}
