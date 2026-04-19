package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// stateDirCfg sets XDG_DATA_HOME so cfg.StateDir() points at a
// tempdir — plugin.list scans cfg.StateDir()/plugins, so the test
// needs to control that. Restores the env on cleanup.
func stateDirCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	return &config.Config{}
}

// TestHeadless_PluginList_Empty: with no plugins directory the list
// RPC returns an empty array, not an error.
func TestHeadless_PluginList_Empty(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	cfg := stateDirCfg(t)
	srv := NewServer(cfg, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"plugin.list"}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"plugins":[]`) {
		t.Errorf("expected empty plugins array: %s", reply)
	}
	client.Close()
}

// TestHeadless_PluginRun_UnknownSession: bad sessionId rejected with
// invalid-params BEFORE any filesystem work happens (fast fail).
func TestHeadless_PluginRun_UnknownSession(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	cfg := stateDirCfg(t)
	srv := NewServer(cfg, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client,
		`{"jsonrpc":"2.0","id":1,"method":"plugin.run","params":{"sessionId":"bogus","id":"foo-0.1.0","tool":"compact"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "unknown sessionId") {
		t.Errorf("expected unknown sessionId error: %s", reply)
	}
	client.Close()
}

// TestHeadless_PluginRun_UnknownPlugin: sessionId resolves but plugin
// isn't installed → invalid-params, no panic.
func TestHeadless_PluginRun_UnknownPlugin(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	cfg := stateDirCfg(t)
	srv := NewServer(cfg, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.run","params":{"sessionId":"h-1","id":"nope-0.0.0","tool":"x"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "not installed") {
		t.Errorf("expected not-installed error: %s", reply)
	}
	client.Close()
}

// TestHeadless_PluginRun_MissingFields: protocol-level validation —
// no id/tool at all should surface an invalid-params error.
func TestHeadless_PluginRun_MissingFields(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	cfg := stateDirCfg(t)
	srv := NewServer(cfg, nil)
	go srv.Serve(context.Background(), server, server)

	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"session.new"}`+"\n")
	_ = readLine(t, client, 2*time.Second)

	io.WriteString(client,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.run","params":{"sessionId":"h-1"}}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, "requires id + tool") {
		t.Errorf("expected id+tool validation error: %s", reply)
	}
	client.Close()
}

// forkTestServer builds a Server with a live hSession backed by a real
// stadogit sidecar + session so forkFn can actually create a child. No
// provider is required — the ForkFn path doesn't touch LLM surface.
func forkTestServer(t *testing.T) (*Server, *hSession) {
	t.Helper()
	baseDir := t.TempDir()
	sidecarPath := filepath.Join(baseDir, "sessions.git")
	worktreeRoot := filepath.Join(baseDir, "worktrees")
	_ = os.MkdirAll(worktreeRoot, 0o755)

	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := stadogit.CreateSession(sc, worktreeRoot, "parent-sess", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "t1"}); err != nil {
		t.Fatal(err)
	}
	emptyTree, err := parent.BuildTreeFromDir(parent.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		Cfg:      &config.Config{},
		sessions: map[string]*hSession{},
	}
	sess := &hSession{
		id:       "h-1",
		workdir:  parent.WorktreePath,
		gitSess:  parent,
		messages: nil,
	}
	srv.sessions[sess.id] = sess
	return srv, sess
}

// TestHeadless_ForkFn_CreatesChildSession: parallel to the TUI's
// TestPluginForkAt_CreatesChildSession but for the headless forkFn
// closure. The child trace ref must exist with the plugin-tagged seed
// commit.
func TestHeadless_ForkFn_CreatesChildSession(t *testing.T) {
	srv, sess := forkTestServer(t)
	fork := srv.forkFn(sess, "auto-compact")

	childID, err := fork(context.Background(), "", "summary")
	if err != nil {
		t.Fatalf("forkFn: %v", err)
	}
	if childID == "" || childID == sess.gitSess.ID {
		t.Errorf("bad child id %q (parent %q)", childID, sess.gitSess.ID)
	}
	h, err := sess.gitSess.Sidecar.ResolveRef(stadogit.TraceRef(childID))
	if err != nil {
		t.Fatalf("child trace ref: %v", err)
	}
	if h == plumbing.ZeroHash {
		t.Error("child trace ref is zero")
	}
}

// TestHeadless_ForkFn_NoGitSession_Errors: defensive path — a headless
// session with no gitSess cannot fork, and the plugin must see the
// error instead of a silent success.
func TestHeadless_ForkFn_NoGitSession_Errors(t *testing.T) {
	srv := &Server{Cfg: &config.Config{}, sessions: map[string]*hSession{}}
	sess := &hSession{id: "h-1"}
	_, err := srv.forkFn(sess, "x")(context.Background(), "", "y")
	if err == nil {
		t.Fatal("expected error without git session")
	}
}

// TestHeadless_ForkFn_EmitsPluginForkNotification: the fork closure
// must push a session.update{kind:"plugin_fork"} notification on the
// server's conn so the JSON-RPC client sees the event. Mirrors the
// TUI's pluginForkMsg path but on the wire.
func TestHeadless_ForkFn_EmitsPluginForkNotification(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv, sess := forkTestServer(t)
	srv.conn = acp.NewConn(server, server)

	// Drain client-side reads on a goroutine so conn.Notify's write
	// doesn't stall on a full pipe.
	lines := make(chan string, 4)
	go func() {
		br := bufio.NewReader(client)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			lines <- line
		}
	}()

	fork := srv.forkFn(sess, "auto-compact")
	if _, err := fork(context.Background(), "", "the summary body"); err != nil {
		t.Fatalf("forkFn: %v", err)
	}

	select {
	case line := <-lines:
		var payload struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("parse: %v, line=%q", err, line)
		}
		if payload.Method != "session.update" {
			t.Errorf("method=%q, want session.update", payload.Method)
		}
		if payload.Params["kind"] != "plugin_fork" {
			t.Errorf("kind=%v, want plugin_fork", payload.Params["kind"])
		}
		if payload.Params["plugin"] != "auto-compact" {
			t.Errorf("plugin=%v, want auto-compact", payload.Params["plugin"])
		}
		if payload.Params["sessionId"] != "h-1" {
			t.Errorf("sessionId=%v, want h-1", payload.Params["sessionId"])
		}
		if _, ok := payload.Params["child"].(string); !ok {
			t.Errorf("child id missing/wrong type: %v", payload.Params["child"])
		}
		if reason, _ := payload.Params["reason"].(string); !strings.Contains(reason, "summary") {
			t.Errorf("reason should contain input: %v", payload.Params["reason"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no session.update notification arrived")
	}
}

// TestTrimSeed_Headless mirrors the TUI's trimSeed tests; both files
// keep their own copy of the helper to avoid a cross-package import
// dependency that'd bloat the headless package for one trivial fn.
func TestTrimSeed_Headless(t *testing.T) {
	cases := []struct {
		in, want string
		max      int
	}{
		{"hello", "hello", 60},
		{"line1\nline2", "line1 line2", 60},
		{"abcdefghij", "abcd…", 5},
	}
	for _, c := range cases {
		if got := trimSeed(c.in, c.max); got != c.want {
			t.Errorf("trimSeed(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

// TestHeadless_BuildBridge_NoProviderNoSession: defensive — without
// any backing, buildBridge returns nil so plugins fail clean at the
// host-import layer rather than panicking on a half-wired bridge.
func TestHeadless_BuildBridge_NoProviderNoSession(t *testing.T) {
	srv := &Server{Cfg: &config.Config{}, sessions: map[string]*hSession{}}
	sess := &hSession{id: "h-1"} // no workdir, no gitSess, no provider
	if got := srv.buildBridge(sess, "x"); got != nil {
		t.Errorf("expected nil bridge, got %+v", got)
	}
}
