package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	reg := BuildDefaultRegistry()
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
	}
}

func TestBundledPluginTool_RunRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello from bundled plugin"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := BuildDefaultRegistry()
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
	reg := BuildDefaultRegistry()
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

func TestBundledPluginTool_ClassPreserved(t *testing.T) {
	reg := BuildDefaultRegistry()
	if got := reg.ClassOf("read"); got != tool.ClassNonMutating {
		t.Fatalf("ClassOf(read) = %v, want %v", got, tool.ClassNonMutating)
	}
	if got := reg.ClassOf("bash"); got != tool.ClassExec {
		t.Fatalf("ClassOf(bash) = %v, want %v", got, tool.ClassExec)
	}
	if got := reg.ClassOf("approval_demo"); got != tool.ClassNonMutating {
		t.Fatalf("ClassOf(approval_demo) = %v, want %v", got, tool.ClassNonMutating)
	}
}
