package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
)

// TestStartupNotices_IncludeHookSkipWarning is the TUI side of the C3
// regression: a broken lifecycle hook is dropped silently because the
// skip-warning goes to stderr, which the alt-screen swallows. The TUI must
// collect the hook skip-warnings (via hooks.BuildLifecycleRunnerWithWarnings)
// and surface them as a startup-notice system block.
//
// This drives the same collector the TUI Run() path uses and then injects the
// result, asserting the warning reaches the scrollback.
func TestStartupNotices_IncludeHookSkipWarning(t *testing.T) {
	cfg := &config.Config{}
	cfg.Hooks.Lifecycle = []config.LifecycleHook{{
		Name: "broken",
		Lua:  "function pre_tool( this is not valid lua",
	}}

	_, warnings := hooks.BuildLifecycleRunnerWithWarnings(cfg)
	if len(warnings) == 0 {
		t.Fatal("collector returned no warnings for a broken hook")
	}

	m := &Model{}
	m.injectStartupNotices(warnings)

	body := m.lastSystemBlockBody()
	if !strings.Contains(body, `skipping lifecycle hook "broken"`) {
		t.Fatalf("startup notices missing hook skip-warning; body=%q", body)
	}
}
