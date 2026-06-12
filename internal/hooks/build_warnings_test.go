package hooks

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestBuildLifecycleRunnerWithWarnings_ReturnsSkipWarning: a broken hook must
// be reported as a returned warning string (not just written to stderr) so a
// screen-owning caller — the alt-screen TUI — can buffer it into the startup
// notices instead of losing it. C3 regression.
func TestBuildLifecycleRunnerWithWarnings_ReturnsSkipWarning(t *testing.T) {
	cfg := &config.Config{}
	cfg.Hooks.Lifecycle = []config.LifecycleHook{
		{Name: "broken", Lua: "function pre_tool( this is not valid lua"},
		{Name: "ok", Lua: "function pre_tool(p) end"},
	}

	runner, warnings := BuildLifecycleRunnerWithWarnings(cfg)
	if runner == nil {
		t.Fatal("the valid hook should still build a runner")
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning for the broken hook, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], `skipping lifecycle hook "broken"`) {
		t.Fatalf("warning text lost the hook name/reason; got %q", warnings[0])
	}
}

// TestBuildLifecycleRunnerWithWarnings_NoWarningsWhenClean: a fully valid
// config returns no warnings, so a clean launch gets no stray startup notice.
func TestBuildLifecycleRunnerWithWarnings_NoWarningsWhenClean(t *testing.T) {
	cfg := &config.Config{}
	cfg.Hooks.Lifecycle = []config.LifecycleHook{
		{Name: "ok", Lua: "function pre_tool(p) end"},
	}
	runner, warnings := BuildLifecycleRunnerWithWarnings(cfg)
	if runner == nil {
		t.Fatal("a valid hook should build a runner")
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for a clean config, got %v", warnings)
	}
}
