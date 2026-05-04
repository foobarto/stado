package runtime_test

// End-to-end test for the persistent-shell example plugin.
// Loads the operator-built plugin.wasm + its manifest, drives the
// full create → attach → write → read → destroy cycle through a
// single Runtime, and verifies that:
//
//   - the wasm-side tool exports correctly call the host imports
//     (no signature drift);
//   - PTY state persists across multiple tool calls in one runtime
//     (the architectural property the plugin depends on);
//   - read returns the bytes the child wrote to stdout, base64-encoded.
//
// Skipped if plugin.wasm or plugin.manifest.json don't exist — those
// are operator-built artifacts (see plugins/examples/persistent-shell/
// build.sh), not committed to the repo. To run locally:
//
//   cd plugins/examples/persistent-shell && ./build.sh
//   go test -count=1 -timeout=30s -run TestPersistentShellPlugin_E2E \
//     ./internal/plugins/runtime/...

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/runtime"
)

func TestPersistentShellPlugin_E2E(t *testing.T) {
	pluginDir := filepath.Join("..", "..", "..", "plugins", "examples", "persistent-shell")
	wasmPath := filepath.Join(pluginDir, "plugin.wasm")
	manifestPath := filepath.Join(pluginDir, "plugin.manifest.json")
	if _, err := os.Stat(wasmPath); err != nil {
		t.Skipf("plugin.wasm not built (%v) — run plugins/examples/persistent-shell/build.sh", err)
	}
	if _, err := os.Stat(manifestPath); err != nil {
		t.Skipf("plugin.manifest.json missing (%v) — run plugins/examples/persistent-shell/build.sh", err)
	}

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read plugin.wasm: %v", err)
	}
	mfBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read plugin.manifest.json: %v", err)
	}
	var mf plugins.Manifest
	if err := json.Unmarshal(mfBytes, &mf); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	ctx := context.Background()
	rt, err := runtime.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	host := runtime.NewHost(mf, t.TempDir(), nil)
	if err := runtime.InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	if !host.ExecPTY {
		t.Fatal("manifest didn't grant exec:pty — sanity check failed")
	}
	mod, err := rt.Instantiate(ctx, wasmBytes, mf)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	// Build per-tool adapters and verify all nine declared tools are
	// addressable.
	tools := make(map[string]*runtime.PluginTool, len(mf.Tools))
	for _, def := range mf.Tools {
		pt, err := runtime.NewPluginTool(mod, def)
		if err != nil {
			t.Fatalf("NewPluginTool %s: %v", def.Name, err)
		}
		tools[def.Name] = pt
	}
	for _, name := range []string{
		"shell_create", "shell_list", "shell_attach", "shell_detach",
		"shell_write", "shell_read", "shell_signal", "shell_resize", "shell_destroy",
	} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("missing tool: %s", name)
		}
	}

	run := func(name, args string) string {
		t.Helper()
		res, err := tools[name].Run(ctx, json.RawMessage(args), host.ToolHost)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Error != "" {
			t.Fatalf("%s: tool error: %s", name, res.Error)
		}
		return res.Content
	}

	// 1. create — `cat` so we can write+read in the same runtime.
	out := run("shell_create", `{"argv":["/bin/cat"]}`)
	var created struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode create: %v (raw=%q)", err, out)
	}
	if created.ID == 0 {
		t.Fatalf("create returned id=0 (raw=%q)", out)
	}

	// 2. list — should now have one entry, alive=true, attached=false.
	listOut := run("shell_list", `{}`)
	var sessions []struct {
		ID       uint64 `json:"id"`
		Alive    bool   `json:"alive"`
		Attached bool   `json:"attached"`
	}
	if err := json.Unmarshal([]byte(listOut), &sessions); err != nil {
		t.Fatalf("decode list: %v (raw=%q)", err, listOut)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("list = %+v, want one entry with id=%d", sessions, created.ID)
	}
	if !sessions[0].Alive || sessions[0].Attached {
		t.Fatalf("list: alive=%v attached=%v, want alive=true attached=false", sessions[0].Alive, sessions[0].Attached)
	}

	// 3. attach — claim it.
	run("shell_attach", `{"id":`+itoa(created.ID)+`}`)

	// 4. write a line to cat.
	run("shell_write", `{"id":`+itoa(created.ID)+`,"data":"hello-from-test\n"}`)

	// 5. read — drain the ring with retries because pty echoing is
	// asynchronous (kernel buffers + cat's input mode).
	got := readUntil(t, run, created.ID, "hello-from-test", 2*time.Second)
	if !strings.Contains(got, "hello-from-test") {
		t.Fatalf("read: got=%q, want substring 'hello-from-test'", got)
	}

	// 6. detach — releases the claim, session stays alive.
	run("shell_detach", `{"id":`+itoa(created.ID)+`}`)

	// 7. destroy — SIGTERM/grace/SIGKILL.
	run("shell_destroy", `{"id":`+itoa(created.ID)+`}`)

	// 8. list again — registry empty.
	listOut = run("shell_list", `{}`)
	if strings.TrimSpace(listOut) != "[]" {
		t.Fatalf("list after destroy = %q, want []", listOut)
	}
}

func readUntil(t *testing.T, run func(name, args string) string, id uint64, want string, total time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(total)
	var acc string
	for time.Now().Before(deadline) {
		out := run("shell_read", `{"id":`+itoa(id)+`,"timeout_ms":300,"max_bytes":4096}`)
		var r struct {
			Data    string `json:"data"`
			DataB64 string `json:"data_b64"`
			N       int    `json:"n"`
			EOF     bool   `json:"eof"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("decode read: %v (raw=%q)", err, out)
		}
		if r.N > 0 {
			if r.Data != "" {
				acc += r.Data
			} else if r.DataB64 != "" {
				if raw, err := base64.StdEncoding.DecodeString(r.DataB64); err == nil {
					acc += string(raw)
				}
			}
		}
		if strings.Contains(acc, want) {
			return acc
		}
		if r.EOF {
			return acc
		}
	}
	return acc
}

// TestPersistentShellPlugin_DetachReattachReplay proves the
// "modern-terminal scrollback" property: a session can run while
// detached, output buffers in the ring, a later attach replays the
// captured bytes. This is the property that makes the plugin useful
// for "reverse-shell catcher" / "long-running listener" workflows
// where the agent does other work between attach windows.
func TestPersistentShellPlugin_DetachReattachReplay(t *testing.T) {
	rt, host, mod, tools := loadPersistentShell(t)
	defer rt.Close(context.Background())
	defer mod.Close(context.Background())
	_ = host

	run := mkRunner(t, tools, host)

	// Spawn a session that prints once and then sleeps. Output lands
	// in the ring buffer while we're detached.
	out := run("shell_create", `{"cmd":"printf detached-payload; sleep 5"}`)
	id := decodeID(t, out)

	// Wait for the printf to land in the ring.
	deadline := time.Now().Add(2 * time.Second)
	var buffered int
	for time.Now().Before(deadline) {
		listOut := run("shell_list", `{}`)
		var sessions []struct {
			ID       uint64 `json:"id"`
			Buffered int    `json:"buffered"`
		}
		_ = json.Unmarshal([]byte(listOut), &sessions)
		for _, s := range sessions {
			if s.ID == id {
				buffered = s.Buffered
				break
			}
		}
		if buffered >= len("detached-payload") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if buffered < len("detached-payload") {
		t.Fatalf("ring didn't accumulate output while detached (buffered=%d)", buffered)
	}

	// Attach now and read the buffered backlog.
	run("shell_attach", `{"id":`+itoa(id)+`}`)
	got := readUntil(t, run, id, "detached-payload", 2*time.Second)
	if !strings.Contains(got, "detached-payload") {
		t.Fatalf("re-attach didn't replay backlog: got=%q", got)
	}

	run("shell_destroy", `{"id":`+itoa(id)+`}`)
}

// TestPersistentShellPlugin_RingOverflow proves bounded memory: a
// session producing more bytes than the ring capacity drops oldest
// bytes silently, and the dropped counter is visible via list.
func TestPersistentShellPlugin_RingOverflow(t *testing.T) {
	rt, host, mod, tools := loadPersistentShell(t)
	defer rt.Close(context.Background())
	defer mod.Close(context.Background())
	_ = host

	run := mkRunner(t, tools, host)

	// 4 KiB ring is the floor (manager.go clamps below it). `yes`
	// fills 4 KiB in microseconds, so dropped should be enormous
	// within a second of detached running.
	out := run("shell_create", `{"argv":["/usr/bin/yes"],"buffer_bytes":4096}`)
	id := decodeID(t, out)
	defer run("shell_destroy", `{"id":`+itoa(id)+`}`)

	// Let yes fill + overflow the ring.
	time.Sleep(200 * time.Millisecond)

	listOut := run("shell_list", `{}`)
	var sessions []struct {
		ID       uint64 `json:"id"`
		Buffered int    `json:"buffered"`
		Dropped  uint64 `json:"dropped"`
	}
	if err := json.Unmarshal([]byte(listOut), &sessions); err != nil {
		t.Fatalf("decode list: %v (raw=%q)", err, listOut)
	}
	var found bool
	for _, s := range sessions {
		if s.ID == id {
			found = true
			if s.Buffered != 4096 {
				t.Fatalf("buffered=%d, want 4096 (ring full)", s.Buffered)
			}
			if s.Dropped == 0 {
				t.Fatalf("dropped=0 after 200ms of `yes`, want >0")
			}
			t.Logf("ring overflow: buffered=%d dropped=%d", s.Buffered, s.Dropped)
		}
	}
	if !found {
		t.Fatalf("session %d not in list", id)
	}
}

// TestPersistentShellPlugin_ForceAttachSteals proves the recovery
// semantics: if a previous attacher crashed without detaching,
// force=true takes the lock without complaint. The non-force path
// returns a tool-level error.
func TestPersistentShellPlugin_ForceAttachSteals(t *testing.T) {
	rt, host, mod, tools := loadPersistentShell(t)
	defer rt.Close(context.Background())
	defer mod.Close(context.Background())
	_ = host

	run := mkRunner(t, tools, host)
	runErr := func(name, args string) string {
		t.Helper()
		res, err := tools[name].Run(context.Background(), json.RawMessage(args), host.ToolHost)
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		// Plugin tool-level errors come back in res.Content as
		// {"error":"..."} — see main.go writeJSON. Return raw.
		return res.Content
	}

	out := run("shell_create", `{"argv":["/bin/cat"]}`)
	id := decodeID(t, out)
	defer run("shell_destroy", `{"id":`+itoa(id)+`}`)

	// First attach — succeeds.
	run("shell_attach", `{"id":`+itoa(id)+`}`)

	// Second attach without force — surfaces ErrAlreadyAttached.
	body := runErr("shell_attach", `{"id":`+itoa(id)+`}`)
	if !strings.Contains(body, "already attached") {
		t.Fatalf("non-force attach: want already-attached error, got %q", body)
	}

	// Third attach with force — succeeds.
	run("shell_attach", `{"id":`+itoa(id)+`,"force":true}`)
}

// loadPersistentShell is the shared setup for every e2e scenario.
// Skips when plugin.wasm isn't built; otherwise returns a wired
// runtime + per-tool adapters.
func loadPersistentShell(t *testing.T) (*runtime.Runtime, *runtime.Host, *runtime.Module, map[string]*runtime.PluginTool) {
	t.Helper()
	pluginDir := filepath.Join("..", "..", "..", "plugins", "examples", "persistent-shell")
	wasmPath := filepath.Join(pluginDir, "plugin.wasm")
	manifestPath := filepath.Join(pluginDir, "plugin.manifest.json")
	if _, err := os.Stat(wasmPath); err != nil {
		t.Skipf("plugin.wasm not built (%v) — run plugins/examples/persistent-shell/build.sh", err)
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read plugin.wasm: %v", err)
	}
	mfBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var mf plugins.Manifest
	if err := json.Unmarshal(mfBytes, &mf); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	ctx := context.Background()
	rt, err := runtime.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	host := runtime.NewHost(mf, t.TempDir(), nil)
	if err := runtime.InstallHostImports(ctx, rt, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
	mod, err := rt.Instantiate(ctx, wasmBytes, mf)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	tools := make(map[string]*runtime.PluginTool, len(mf.Tools))
	for _, def := range mf.Tools {
		pt, err := runtime.NewPluginTool(mod, def)
		if err != nil {
			t.Fatalf("NewPluginTool %s: %v", def.Name, err)
		}
		tools[def.Name] = pt
	}
	return rt, host, mod, tools
}

func mkRunner(t *testing.T, tools map[string]*runtime.PluginTool, host *runtime.Host) func(name, args string) string {
	return func(name, args string) string {
		t.Helper()
		res, err := tools[name].Run(context.Background(), json.RawMessage(args), host.ToolHost)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Tool-level errors are JSON-encoded in Content as
		// {"error":"..."}; promote those to test failures so the
		// scenario tests don't silently pass through them.
		if strings.HasPrefix(res.Content, `{"error":`) {
			t.Fatalf("%s: tool error: %s", name, res.Content)
		}
		if res.Error != "" {
			t.Fatalf("%s: transport error: %s", name, res.Error)
		}
		return res.Content
	}
}

func decodeID(t *testing.T, out string) uint64 {
	t.Helper()
	var s struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("decode id: %v (raw=%q)", err, out)
	}
	if s.ID == 0 {
		t.Fatalf("id=0 (raw=%q)", out)
	}
	return s.ID
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
