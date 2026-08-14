package pluginrun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
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

func TestRunRejectsMissingCanonicalIdentity(t *testing.T) {
	manifest := plugins.Manifest{Name: "display-only", Version: "dev"}
	result, err := Run(context.Background(), RunArgs{Manifest: manifest}, testHost{wd: t.TempDir()})
	if err == nil {
		t.Fatal("missing runtime identity unexpectedly accepted")
	}
	if result.Error == "" {
		t.Fatalf("missing identity error was not returned in result: %+v", result)
	}
}

func TestRunRejectsIdentityBoundToDifferentManifest(t *testing.T) {
	identityManifest := plugins.Manifest{Name: "display-one", Version: "dev"}
	identity, err := plugins.RuntimeIdentityForLocalSource(identityManifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executedManifest := identityManifest
	executedManifest.Name = "display-two"
	result, err := Run(context.Background(), RunArgs{Manifest: executedManifest, Identity: identity}, testHost{wd: t.TempDir()})
	if err == nil {
		t.Fatal("identity for another manifest unexpectedly accepted")
	}
	if result.Error == "" {
		t.Fatalf("identity mismatch was not returned in result: %+v", result)
	}
}

func TestAttachAuthorityNamespacesUsesCanonicalIdentity(t *testing.T) {
	manifest := plugins.Manifest{
		Name:         "display-only",
		Version:      "dev",
		Capabilities: []string{"secrets:read", "state:read"},
	}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := pluginRuntime.NewHostWithIdentity(manifest, identity, t.TempDir(), nil)
	host.AttachAuthorityStores(t.TempDir(), pluginRuntime.NewInstanceStore(), nil)
	if host.Secrets == nil || host.Secrets.PluginName != identity.Namespace {
		t.Fatalf("secret namespace = %+v, want %q", host.Secrets, identity.Namespace)
	}
	if host.State == nil || host.State.PluginName != identity.Namespace {
		t.Fatalf("state namespace = %+v, want %q", host.State, identity.Namespace)
	}
	if got := host.AuditIdentity(); got != identity.Canonical {
		t.Fatalf("audit identity = %q, want %q", got, identity.Canonical)
	}
}
