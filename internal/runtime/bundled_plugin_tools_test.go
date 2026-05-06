package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

type bundledToolHost struct {
	tools.NullHost
	workdir string
	runner  sandbox.Runner
}

func (h bundledToolHost) Workdir() string { return h.workdir }

func (h bundledToolHost) Runner() sandbox.Runner { return h.runner }

// bundledToolHostWithPTY mirrors bundledToolHost but additionally
// implements pkg/tool.PTYProvider, exposing a shared PTY manager so
// successive bundledPluginTool.Run calls see the same session
// registry. Used by the cross-call PTY persistence regression test.
type bundledToolHostWithPTY struct {
	bundledToolHost
	pty *pty.Manager
}

func (h bundledToolHostWithPTY) PTYManager() any { return h.pty }

type recordingRunner struct {
	called bool
	policy sandbox.Policy
}

func (r *recordingRunner) Name() string    { return "recording" }
func (r *recordingRunner) Available() bool { return true }
func (r *recordingRunner) Command(ctx context.Context, p sandbox.Policy, cmd string, args []string, env []string) (*exec.Cmd, error) {
	r.called = true
	r.policy = p
	return exec.CommandContext(ctx, "bash", "-lc", "printf runner-ok"), nil
}

func TestBuildDefaultRegistry_UsesBundledPluginTools(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	got, ok := reg.Get("read")
	if !ok {
		t.Fatal("read tool missing")
	}
	if pt, ok := got.(*bundledPluginTool); !ok {
		t.Fatalf("read tool type = %T, want *bundledPluginTool", got)
	} else if len(pt.manifest.Capabilities) != 1 || pt.manifest.Capabilities[0] != "fs:read:." {
		t.Fatalf("read capabilities = %v, want [fs:read:.]", pt.manifest.Capabilities)
	}
	if got, ok := reg.Get("approval_demo"); !ok {
		t.Fatal("approval_demo tool missing")
	} else if pt, ok := got.(*bundledPluginTool); !ok {
		t.Fatalf("approval_demo type = %T, want *bundledPluginTool", got)
	} else if len(pt.manifest.Capabilities) != 1 || pt.manifest.Capabilities[0] != "ui:approval" {
		t.Fatalf("approval_demo capabilities = %v, want [ui:approval]", pt.manifest.Capabilities)
	} else if !strings.Contains(got.Description(), "Manual test tool only") {
		t.Fatalf("approval_demo description should warn AI away, got %q", got.Description())
	}
	if got, ok := reg.Get("agent__spawn"); !ok {
		t.Fatal("agent__spawn tool missing")
	} else if _, ok := got.(*renamedTool); !ok {
		t.Fatalf("agent__spawn type = %T, want *renamedTool", got)
	}
}

func TestBundledPluginTool_RunRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello from bundled plugin"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := BuildDefaultRegistry(nil)
	got, ok := reg.Get("read")
	if !ok {
		t.Fatal("read tool missing")
	}
	res, err := got.Run(context.Background(), json.RawMessage(`{"path":"note.txt"}`), bundledToolHost{workdir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "hello from bundled plugin") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestBundledPluginTool_BashUsesRunner(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	got, ok := reg.Get("bash")
	if !ok {
		t.Fatal("bash tool missing")
	}
	runner := &recordingRunner{}
	host := bundledToolHost{workdir: t.TempDir(), runner: runner}
	res, err := got.Run(context.Background(), json.RawMessage(`{"command":"printf ignored"}`), host)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if !runner.called {
		t.Fatal("runner was not invoked")
	}
	if runner.policy.CWD != host.workdir {
		t.Fatalf("policy.CWD = %q, want %q", runner.policy.CWD, host.workdir)
	}
	if len(runner.policy.Exec) != 1 || runner.policy.Exec[0] != "bash" {
		t.Fatalf("policy.Exec = %v, want [bash]", runner.policy.Exec)
	}
	if !strings.Contains(res.Content, "runner-ok") {
		t.Fatalf("content = %q, want runner output", res.Content)
	}
}

// TestBundledPluginTool_HonoursPTYProvider: cross-call PTY persistence.
// When the host implements tool.PTYProvider, successive bundled-plugin
// dispatches see the same PTY registry — shell.spawn → shell.list
// across calls finds the spawned session. Pre-fix, each Run built a
// fresh pluginRuntime with its own pty.NewManager and the second call
// got an empty list.
func TestBundledPluginTool_HonoursPTYProvider(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires `sleep` binary")
	}
	sharedMgr := pty.NewManager()
	defer sharedMgr.CloseAll()

	host := bundledToolHostWithPTY{
		bundledToolHost: bundledToolHost{
			workdir: t.TempDir(),
			runner:  sandbox.NoneRunner{},
		},
		pty: sharedMgr,
	}

	reg := BuildDefaultRegistry(nil)
	listTool, ok := reg.Get("shell__list")
	if !ok {
		t.Fatal("shell__list missing from registry")
	}

	// First dispatch — empty manager.
	res1, err := listTool.Run(context.Background(), json.RawMessage(`{}`), host)
	if err != nil {
		t.Fatalf("first list: %v", err)
	}

	// Pre-populate the SHARED manager with a real session so the
	// second list call has something to find. Spawn directly via
	// the manager — bundled wasm shell.spawn would do the same
	// against the shared manager, but spawning here lets us pin
	// the assertion to a known id.
	id, err := sharedMgr.Spawn(pty.SpawnOpts{Cmd: "sleep 30"})
	if err != nil {
		t.Skipf("Spawn requires runnable shell environment: %v", err)
	}
	defer sharedMgr.Destroy(id)
	idStr := strconv.FormatUint(id, 10)

	// Second dispatch — should observe the shared session.
	res2, err := listTool.Run(context.Background(), json.RawMessage(`{}`), host)
	if err != nil {
		t.Fatalf("second list: %v", err)
	}
	if !strings.Contains(res2.Content, idStr) {
		t.Errorf("second list did not see the shared PTY id %s\nfirst:  %s\nsecond: %s",
			idStr, res1.Content, res2.Content)
	}
}

func TestBundledPluginTool_ClassPreserved(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	if got := reg.ClassOf("read"); got != tool.ClassNonMutating {
		t.Fatalf("ClassOf(read) = %v, want %v", got, tool.ClassNonMutating)
	}
	if got := reg.ClassOf("bash"); got != tool.ClassExec {
		t.Fatalf("ClassOf(bash) = %v, want %v", got, tool.ClassExec)
	}
	if got := reg.ClassOf("approval_demo"); got != tool.ClassNonMutating {
		t.Fatalf("ClassOf(approval_demo) = %v, want %v", got, tool.ClassNonMutating)
	}
	if got := reg.ClassOf("agent__spawn"); got != tool.ClassExec {
		t.Fatalf("ClassOf(agent__spawn) = %v, want %v", got, tool.ClassExec)
	}
}
