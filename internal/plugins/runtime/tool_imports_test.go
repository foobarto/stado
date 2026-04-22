package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/builtinplugins"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/tools"
)

type toolImportHost struct {
	tools.NullHost
	workdir string
}

func (h toolImportHost) Workdir() string { return h.workdir }

type approvalBridgeStub struct {
	allow bool
}

func (s approvalBridgeStub) RequestApproval(context.Context, string, string) (bool, error) {
	return s.allow, nil
}

func TestPublicToolImports_DenyWithoutCapability(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	mf := plugins.Manifest{
		Name:    "third-party",
		Version: "1.0.0",
		Tools:   []plugins.ToolDef{{Name: "read", Class: "NonMutating"}},
	}
	host := NewHost(mf, t.TempDir(), nil)
	host.ToolHost = toolImportHost{workdir: t.TempDir()}
	if err := InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	_, err = rt.Instantiate(ctx, builtinplugins.MustWasm("read"), mf)
	if err == nil {
		t.Fatal("expected instantiate to fail without fs:read capability")
	}
	if !strings.Contains(err.Error(), "stado_fs_tool_read") {
		t.Fatalf("instantiate error = %v, want missing stado_fs_tool_read import", err)
	}
}

func TestPublicToolImports_ReadWorksWithCapability(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	mf := plugins.Manifest{
		Name:         "third-party",
		Version:      "1.0.0",
		Capabilities: []string{"fs:read:."},
		Tools:        []plugins.ToolDef{{Name: "read", Class: "NonMutating"}},
	}
	host := NewHost(mf, dir, nil)
	host.ToolHost = toolImportHost{workdir: dir}
	if err := InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	mod, err := rt.Instantiate(ctx, builtinplugins.MustWasm("read"), mf)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	pt, err := NewPluginTool(mod, mf.Tools[0])
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	res, err := pt.Run(ctx, json.RawMessage(`{"path":"x.txt"}`), host.ToolHost)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %q", res.Error)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("content = %q, want file contents", res.Content)
	}
}

func TestPublicToolImports_ApprovalDemoWorksWithCapability(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	mf := plugins.Manifest{
		Name:         "third-party",
		Version:      "1.0.0",
		Capabilities: []string{"ui:approval"},
		Tools: []plugins.ToolDef{{
			Name:  "approval_demo",
			Class: "NonMutating",
		}},
	}
	host := NewHost(mf, t.TempDir(), nil)
	host.ApprovalBridge = approvalBridgeStub{allow: true}
	if err := InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	mod, err := rt.Instantiate(ctx, builtinplugins.MustWasm("approval_demo"), mf)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	pt, err := NewPluginTool(mod, mf.Tools[0])
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	res, err := pt.Run(ctx, json.RawMessage(`{"title":"demo","body":"continue?"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %q", res.Error)
	}
	if res.Content != "approved" {
		t.Fatalf("content = %q, want approved", res.Content)
	}
}
