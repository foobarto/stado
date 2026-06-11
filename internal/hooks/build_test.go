package hooks

import (
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestBuildLifecycleRunner_ThreadsFailClosed: the [hooks].fail_closed knob
// must reach the constructed runner. Default config (fail_closed unset)
// yields a fail-open runner; setting it yields a fail-closed one.
func TestBuildLifecycleRunner_ThreadsFailClosed(t *testing.T) {
	mkCfg := func(failClosed bool) *config.Config {
		c := &config.Config{}
		c.Hooks.Lifecycle = []config.LifecycleHook{{
			Name: "p",
			Lua:  "function pre_tool(p) end",
		}}
		c.Hooks.FailClosed = failClosed
		return c
	}

	open := BuildLifecycleRunner(mkCfg(false))
	if open == nil {
		t.Fatal("runner should be built for a configured hook")
	}
	if open.FailClosed {
		t.Fatal("default config must yield a fail-open runner")
	}

	closed := BuildLifecycleRunner(mkCfg(true))
	if closed == nil {
		t.Fatal("runner should be built for a configured hook")
	}
	if !closed.FailClosed {
		t.Fatal("fail_closed=true must thread into the runner")
	}
}
