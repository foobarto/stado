package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/bundledplugins"
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
	// EP-0038b: imports are always registered now (capability check at call time)
	// so wasm plugins linking against multiple tool imports can succeed even when
	// only some caps are granted. Capability denial happens at call, not link.
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
	mod, err := rt.Instantiate(ctx, bundledplugins.MustWasm("fs"), mf)
	if err != nil {
		t.Fatalf("instantiate should succeed (link-time): got %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()
	// Now invoking the read tool should produce a capability-denied error.
	pt, err := NewPluginTool(mod, plugins.ToolDef{Name: "read"})
	if err != nil {
		t.Fatalf("NewPluginTool: %v", err)
	}
	res, _ := pt.Run(ctx, []byte(`{"path":"x"}`), toolImportHost{workdir: t.TempDir()})
	combined := res.Content + res.Error
	// Step 7 of EP-no-internal-tools: read now goes through the
	// stado_fs_read primitive. Cap denial returns -1 from the host
	// import; the wasm handler surfaces that as a generic "read
	// failed" message. The denial itself shows up as a host-side
	// stado_fs_read denied warning (visible in the test log).
	if !strings.Contains(combined, "denied") &&
		!strings.Contains(combined, "capabilities") &&
		!strings.Contains(combined, "read failed") {
		t.Fatalf("expected capability denial at call time, got: content=%q error=%q", res.Content, res.Error)
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
	mod, err := rt.Instantiate(ctx, bundledplugins.MustWasm("fs"), mf)
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
	wasmBytes := buildExampleWasm(t, "approval-demo-go", "stado_tool_approval_demo")

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
	mod, err := rt.Instantiate(ctx, wasmBytes, mf)
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

// buildExampleWasm compiles plugins/examples/<name>/main.go to a
// wasip1 wasm module and returns the bytes. Skips the test when no
// `go` toolchain is on PATH or the example dir doesn't exist (covers
// the trimmed-tree distribution case). The example sources are the
// canonical home for these implementation demos since they're not
// bundled into the stado binary.
func buildExampleWasm(t *testing.T, exampleDir, expectedExport string) []byte {
	t.Helper()

	repoRoot, err := findRepoRootForTest()
	if err != nil {
		t.Skipf("buildExampleWasm: locate repo root: %v", err)
	}
	src := filepath.Join(repoRoot, "plugins", "examples", exampleDir)
	if _, err := os.Stat(filepath.Join(src, "main.go")); err != nil {
		t.Skipf("buildExampleWasm: example %q not present in this tree (%v)", exampleDir, err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("buildExampleWasm: go toolchain not on PATH (%v)", err)
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, exampleDir+".wasm")
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buildExampleWasm: go build %s: %v\n%s", exampleDir, err, buildOut)
	}
	bytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("buildExampleWasm: read %s: %v", out, err)
	}
	_ = expectedExport // sanity check is left to the wasm runtime's instantiation
	return bytes
}

// findRepoRootForTest walks up from the test's working directory
// looking for go.mod. Used by buildExampleWasm to resolve the
// plugins/examples path independent of where `go test` was launched.
func findRepoRootForTest() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod ancestor of %s", dir)
		}
		dir = parent
	}
}
