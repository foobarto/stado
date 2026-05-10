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
	got, ok := reg.Get("fs__read")
	if !ok {
		t.Fatal("read tool missing")
	}
	// Step 7 of EP-no-internal-tools: fs__read is now a wasm tool
	// registered via newBundledWasmTool, which wraps in renamedTool
	// (not the legacy *bundledPluginTool that wrapped natives).
	rt, ok := got.(*renamedTool)
	if !ok {
		t.Fatalf("fs__read type = %T, want *renamedTool", got)
	}
	pt, ok := rt.inner.(*bundledPluginTool)
	if !ok {
		t.Fatalf("renamedTool.inner = %T, want *bundledPluginTool", rt.inner)
	}
	if len(pt.manifest.Capabilities) != 1 || pt.manifest.Capabilities[0] != "fs:read:." {
		t.Fatalf("fs__read capabilities = %v, want [fs:read:.]", pt.manifest.Capabilities)
	}
	// approval_demo / choose_demo are no longer bundled — they live as
	// implementation demos under plugins/optional/{approval-demo-go,
	// choose-demo-go} and must NOT appear in the default registry.
	if _, ok := reg.Get("approval_demo"); ok {
		t.Error("approval_demo should not be in the bundled registry; it is a plugins/optional/ demo")
	}
	if _, ok := reg.Get("choose_demo"); ok {
		t.Error("choose_demo should not be in the bundled registry; it is a plugins/optional/ demo")
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
	got, ok := reg.Get("fs__read")
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

// TestBundledPluginTool_BashUsesRunner removed Step 4 of EP-no-internal-tools.
// The native bash.BashTool routed through sandbox.Runner with a hardcoded
// bwrap policy (workdir + /tmp + net deny). Post-Step-4 the bash tool is
// the wasm shell__bash, which uses stado_exec without sandbox by default —
// plugin author opts in via the new sandbox arg. The "bash automatically
// gets bwrap" behavior this test asserted is gone, intentionally.

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

// TestBundledShellExpect_RoundTripsThroughWasm: shell__expect dispatched
// via the bundled wasm path returns the host's match envelope unchanged.
// Drives the path the agent actually uses: tool registry → wasm wrapper →
// stado_terminal_expect → manager.Expect → JSON response.
func TestBundledShellExpect_RoundTripsThroughWasm(t *testing.T) {
	if _, err := exec.LookPath("printf"); err != nil {
		t.Skip("requires `printf` binary")
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
	expectTool, ok := reg.Get("shell__expect")
	if !ok {
		t.Fatal("shell__expect missing from registry")
	}

	id, err := sharedMgr.Spawn(pty.SpawnOpts{Cmd: "printf 'PROMPT> '; sleep 30"})
	if err != nil {
		t.Skipf("Spawn requires runnable shell environment: %v", err)
	}
	defer sharedMgr.Destroy(id)
	if err := sharedMgr.Attach(id, pty.AttachOpts{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	args := json.RawMessage(`{"id":` + strconv.FormatUint(id, 10) + `,"patterns":["PROMPT> "],"timeout_ms":2000}`)
	res, err := expectTool.Run(context.Background(), args, host)
	if err != nil {
		t.Fatalf("expect Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("expect tool error: %s (content=%s)", res.Error, res.Content)
	}

	var got struct {
		Matched      bool   `json:"matched"`
		PatternIndex int    `json:"pattern_index"`
		Match        string `json:"match"`
	}
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatalf("response not JSON: %v\ncontent=%q", err, res.Content)
	}
	if !got.Matched {
		t.Fatalf("Matched=false; want true. content=%q", res.Content)
	}
	if got.PatternIndex != 0 {
		t.Errorf("PatternIndex=%d; want 0", got.PatternIndex)
	}
	// match field is base64-encoded "PROMPT> ".
	wantB64 := "UFJPTVBUPiA="
	if got.Match != wantB64 {
		t.Errorf("match=%q; want %q (base64 of 'PROMPT> ')", got.Match, wantB64)
	}
}

func TestBundledPluginTool_ClassPreserved(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	if got := reg.ClassOf("fs__read"); got != tool.ClassNonMutating {
		t.Fatalf("ClassOf(fs__read) = %v, want %v", got, tool.ClassNonMutating)
	}
	if got := reg.ClassOf("shell__bash"); got != tool.ClassExec {
		t.Fatalf("ClassOf(shell__bash) = %v, want %v", got, tool.ClassExec)
	}
	if got := reg.ClassOf("agent__spawn"); got != tool.ClassExec {
		t.Fatalf("ClassOf(agent__spawn) = %v, want %v", got, tool.ClassExec)
	}
}

// TestBundledWebFetch_PropagatesHostStructuredError reproduces the
// AC2 bug: pre-fix, web__fetch dropped the host-side error message
// and emitted a useless "stado_http_request returned -1". The host
// uses a negative-return convention (see
// internal/plugins/runtime/host_imports.go::encodeToolSidePayload)
// to signal "this is an error message of length |-n| bytes already
// in the response buffer." Every other plugin in this repo
// (browser, http-session, web-search, mcp-client) reads back
// respBuf[:-n]; the bundled web module didn't.
//
// We trigger the failure path by pointing web.fetch at an
// RFC1918/loopback address: the bundled web manifest declares
// net:http_request (broad public) but NOT net:http_request_private,
// so the dial guard inside httpreq.Do refuses with a structured
// error that the host then writes back via encodeToolSidePayload.
// Pre-fix the operator saw "returned -1"; post-fix they see the
// real reason (e.g. mention of the URL or "private" / "denied" /
// "refused" / "blocked").
func TestBundledWebFetch_PropagatesHostStructuredError(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	got, ok := reg.Get("web__fetch")
	if !ok {
		t.Fatal("web__fetch missing from registry")
	}

	// Loopback URL on a closed port. With NetHTTPRequestPrivate=false
	// (default for the bundled web manifest), the dial guard refuses
	// before any TCP attempt, producing a deterministic error
	// message regardless of whether port 1 is actually listening.
	res, err := got.Run(context.Background(),
		json.RawMessage(`{"url":"http://127.0.0.1:1/"}`),
		bundledToolHost{workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run returned err (expected structured error in res.Content/Error): %v", err)
	}

	body := res.Content
	if body == "" {
		body = res.Error
	}
	t.Logf("web.fetch error body: %q", body)

	// Pre-fix marker — must NOT appear post-fix.
	if strings.Contains(body, "stado_http_request returned -1") {
		t.Fatalf("regression: web.fetch is still dropping the host's structured error and emitting the generic text; got: %q", body)
	}
	// Post-fix the surface should mention the host (127.0.0.1) OR a
	// private-address rejection word. We accept either since the
	// exact wording lives in httpreq.Do and may evolve; what we're
	// asserting is "the host-side reason actually propagates," not
	// its precise phrasing.
	hostMentioned := strings.Contains(body, "127.0.0.1")
	low := strings.ToLower(body)
	privateMentioned := strings.Contains(low, "private") ||
		strings.Contains(low, "loopback") ||
		strings.Contains(low, "denied") ||
		strings.Contains(low, "refused") ||
		strings.Contains(low, "blocked") ||
		strings.Contains(low, "rfc1918")
	if !hostMentioned && !privateMentioned {
		t.Errorf("expected host-side reason in error body; got: %q", body)
	}
}
