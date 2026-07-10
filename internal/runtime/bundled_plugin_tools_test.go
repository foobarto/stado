package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/astgrep"
	"github.com/foobarto/stado/internal/broker"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/rg"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/binext"
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

type bundledToolSandboxHost struct {
	bundledToolHost
	defaultPolicy any
}

func (h bundledToolSandboxHost) DefaultSandboxPolicy() any { return h.defaultPolicy }

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
	// EP-0043 D1-D6: shell.screenshot folded into shell.read (mode:screen)
	// and the attach/detach tools removed. These wire names must NOT be
	// registered — agents that pattern-match on the old tool list (or a
	// stale model that still emits them) should get a clean "no such tool",
	// not a half-wired dispatch.
	for _, dead := range []string{"shell__screenshot", "shell__attach", "shell__detach"} {
		if _, ok := reg.Get(dead); ok {
			t.Errorf("%s should not be in the bundled registry (removed by EP-0043)", dead)
		}
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

func TestBundledShellOneShotToolsAuthorizeTheirLiteralShell(t *testing.T) {
	dir := t.TempDir()
	reg := BuildDefaultRegistry(nil)
	host := bundledToolHost{workdir: dir, runner: sandbox.NoneRunner{}}

	for _, name := range []string{"shell__exec", "shell__sh", "shell__bash"} {
		t.Run(name, func(t *testing.T) {
			got, ok := reg.Get(name)
			if !ok {
				t.Fatalf("%s missing", name)
			}
			res, err := got.Run(context.Background(), json.RawMessage(`{"command":"printf STADO_PROCESS_OK"}`), host)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Error != "" {
				t.Fatalf("tool error: %s", res.Error)
			}
			if !strings.Contains(res.Content, "STADO_PROCESS_OK") {
				t.Fatalf("content = %q", res.Content)
			}
		})
	}
}

func TestBundledShellOneShotReportsNonZeroExit(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	shell, ok := reg.Get("shell__exec")
	if !ok {
		t.Fatal("shell__exec missing")
	}
	res, err := shell.Run(context.Background(), json.RawMessage(`{"command":"printf gate-failed; exit 7"}`),
		bundledToolHost{workdir: t.TempDir(), runner: sandbox.NoneRunner{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Error, "command exited with code 7") || !strings.Contains(res.Error, "gate-failed") {
		t.Fatalf("non-zero exit result = %+v", res)
	}
}

func TestBundledShellOneShotRejectsTruncatedHostResponse(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	exec := &tools.Executor{Registry: reg, Runner: sandbox.NoneRunner{}}
	out := RunVerificationRound(context.Background(), exec,
		bundledToolHost{workdir: t.TempDir(), runner: sandbox.NoneRunner{}},
		VerifyConfig{Commands: []string{"head -c 1100000 /dev/zero; exit 9"}, MaxRounds: 1}, 1, nil)
	if out.Status != VerifyFailed || !strings.Contains(out.Output, "command exited with code 9") ||
		!strings.Contains(out.Output, "response exceeds") {
		t.Fatalf("oversized non-zero verification outcome = %+v", out)
	}
}

func TestBundledShellExecRunsUnderBrokerCeiling(t *testing.T) {
	if sandbox.Detect().Name() != "bwrap" {
		t.Skipf("requires bwrap; runner is %q", sandbox.Detect().Name())
	}
	dir := t.TempDir()
	ceiling := broker.MountTableFor(broker.ProfileDefault, dir).ToPolicy()
	host := bundledToolSandboxHost{
		bundledToolHost: bundledToolHost{
			workdir: dir,
			runner:  sandbox.NewCeilingRunner(sandbox.Detect(), ceiling),
		},
		defaultPolicy: pluginRuntime.NewDefaultSandboxPolicy(dir),
	}

	got, ok := BuildDefaultRegistry(nil).Get("shell__exec")
	if !ok {
		t.Fatal("shell__exec missing")
	}
	res, err := got.Run(context.Background(), json.RawMessage(`{"command":"printf STADO_PROCESS_OK"}`), host)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "STADO_PROCESS_OK") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestBundledSearchToolsExecuteResolvedBinary(t *testing.T) {
	requireBundledOrPATHBinary(t, "rg", rg.BundledPath)
	requireBundledOrPATHBinary(t, "ast-grep", astgrep.BundledPath)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("STADO_RG_PROBE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte("package probe\nfunc f() { println(\"STADO_AST_PROBE\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := BuildDefaultRegistry(nil)
	host := bundledToolHost{workdir: dir, runner: sandbox.NoneRunner{}}

	rgTool, ok := reg.Get("rg__search")
	if !ok {
		t.Fatal("rg__search missing")
	}
	rgResult, err := rgTool.Run(context.Background(), json.RawMessage(`{"pattern":"STADO_RG_PROBE","path":"."}`), host)
	if err != nil || rgResult.Error != "" || !strings.Contains(rgResult.Content, "probe.txt") {
		t.Fatalf("rg resolved-binary result=%+v err=%v", rgResult, err)
	}

	astTool, ok := reg.Get("astgrep__search")
	if !ok {
		t.Fatal("astgrep__search missing")
	}
	astResult, err := astTool.Run(context.Background(), json.RawMessage(`{"pattern":"println($X)","lang":"go","path":"."}`), host)
	if err != nil || astResult.Error != "" || !strings.Contains(astResult.Content, "probe.go") {
		t.Fatalf("astgrep resolved-binary result=%+v err=%v", astResult, err)
	}
}

func requireBundledOrPATHBinary(t *testing.T, name string, bundledPath func() (string, error)) {
	t.Helper()
	if _, err := bundledPath(); err == nil {
		return
	} else if !errors.Is(err, binext.ErrNotBundled) {
		t.Fatalf("resolve bundled %s: %v", name, err)
	}
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is neither embedded nor available on PATH", name)
	}
}

// The wasm shell tools call stado_exec. The surface host supplies the default
// process policy and runner; a bare test host can still omit that policy.

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

// TestBundledShellReadUntil_RoundTripsThroughWasm: shell__read_until
// dispatched via the bundled wasm path returns the host's match envelope
// unchanged. Drives the path the agent actually uses: tool registry → wasm
// wrapper → stado_terminal_expect → manager.Expect → JSON response. (The
// host import keeps the name stado_terminal_expect; only the agent-facing
// tool was renamed expect → read_until.)
func TestBundledShellReadUntil_RoundTripsThroughWasm(t *testing.T) {
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
	expectTool, ok := reg.Get("shell__read_until")
	if !ok {
		t.Fatal("shell__read_until missing from registry")
	}

	id, err := sharedMgr.Spawn(pty.SpawnOpts{Cmd: "printf 'PROMPT> '; sleep 30"})
	if err != nil {
		t.Skipf("Spawn requires runnable shell environment: %v", err)
	}
	defer sharedMgr.Destroy(id)
	// EP-0043 D6: no Attach — read_until (and read/write) reach the session
	// by id alone now.

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
