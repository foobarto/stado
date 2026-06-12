package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

// TestRun_EmitsHookSkipWarningBeforeProviderBuild is the C3 regression: a
// broken lifecycle hook is dropped silently in `stado run` because the
// skip-warning emission (via BuildLifecycleRunner) was gated BEHIND a
// successful provider build. A first-run user with no API key errors out at
// provider build and never learns the hook is broken.
//
// The fix moves the lifecycle-runner build (and its skip-warnings) BEFORE the
// provider build, so the warning reaches stderr regardless of provider config.
// This test stubs runBuildProvider to FAIL (the missing-key case) and asserts
// the skip-warning is still emitted.
func TestRun_EmitsHookSkipWarningBeforeProviderBuild(t *testing.T) {
	oldLoadConfig := runLoadConfig
	oldBuildProvider := runBuildProvider
	oldAgentLoop := runAgentLoop
	oldPrompt, oldSkill, oldSessionID := runPrompt, runSkill, runSessionID
	oldMaxTurns, oldJSON, oldNoTools, oldNoSandbox := runMaxTurns, runJSON, runNoTools, noSandbox
	defer func() {
		runLoadConfig = oldLoadConfig
		runBuildProvider = oldBuildProvider
		runAgentLoop = oldAgentLoop
		runPrompt, runSkill, runSessionID = oldPrompt, oldSkill, oldSessionID
		runMaxTurns, runJSON, runNoTools, noSandbox = oldMaxTurns, oldJSON, oldNoTools, oldNoSandbox
	}()

	runLoadConfig = func() (*config.Config, error) {
		cfg := &config.Config{}
		cfg.Defaults.Model = "test-model"
		// A lifecycle hook that fails to compile — must be SKIPPED with a
		// user-visible warning.
		cfg.Hooks.Lifecycle = []config.LifecycleHook{{
			Name: "broken",
			Lua:  "function pre_tool( this is not valid lua",
		}}
		return cfg, nil
	}
	// Provider build FAILS — simulates the no-API-key first run. Under the
	// old ordering this short-circuits before the hook warning is emitted.
	runBuildProvider = func(*config.Config) (agent.Provider, error) {
		return nil, fmt.Errorf("no API key configured")
	}
	runAgentLoop = func(_ context.Context, opts runtime.AgentLoopOptions) (string, []agent.Message, error) {
		t.Fatal("agent loop should not run when provider build fails")
		return "", nil, nil
	}

	runPrompt = "hi"
	runSkill = ""
	runSessionID = ""
	runMaxTurns = 1
	runJSON = true
	runNoTools = true
	noSandbox = true

	restore := chdir(t, t.TempDir())
	defer restore()

	runCmd.SetContext(context.Background())

	_, stderr := captureOutput(t, func() {
		// Expected to return the provider error — that's fine; we only
		// care that the hook skip-warning was emitted FIRST.
		_ = runCmd.RunE(runCmd, nil)
	})

	if !strings.Contains(stderr, "skipping lifecycle hook \"broken\"") {
		t.Fatalf("expected hook skip-warning on stderr even though provider build failed; got stderr=%q", stderr)
	}
}
