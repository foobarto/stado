package pluginrun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

type stubTool struct {
	name string
}

func (s stubTool) Name() string        { return s.name }
func (s stubTool) Description() string { return "stub" }
func (s stubTool) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s stubTool) Class() tool.Class { return tool.ClassNonMutating }
func (s stubTool) Run(_ context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

type testHost struct {
	tools.NullHost
	wd string
}

func (h testHost) Workdir() string { return h.wd }

func TestMakeInvokeCallback_PrefersExecutor(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(stubTool{name: "stub"})

	var hookLog []string
	hookRunner := hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "record",
		Subscribed: []hooks.Point{hooks.PointPreTool},
		Fn: func(context.Context, hooks.Point, hooks.Payload) (hooks.HookResult, error) {
			hookLog = append(hookLog, "fired")
			return hooks.Continue(), nil
		},
	})
	exec := &tools.Executor{Registry: reg, Hooks: hookRunner, Agent: "test"}

	host := testHost{wd: t.TempDir()}
	cb := makeInvokeCallback(reg, exec, host)
	if _, err := cb(context.Background(), "stub", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("executor path: %v", err)
	}
	if len(hookLog) != 1 {
		t.Fatalf("expected pre_tool hook via executor; log=%v", hookLog)
	}

	hookLog = nil
	cbReg := makeInvokeCallback(reg, nil, host)
	if _, err := cbReg(context.Background(), "stub", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("registry path: %v", err)
	}
	if len(hookLog) != 0 {
		t.Fatalf("registry path should not fire executor hooks; log=%v", hookLog)
	}
}
