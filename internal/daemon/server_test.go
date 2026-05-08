package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// startTestServer is the shared boilerplate for the rest of the file:
// pick a sockpath under t.TempDir(), boot a Server in a goroutine, and
// hand the caller a ready Client + a teardown closure.
func startTestServer(t *testing.T, opts ServerOpts) (*Server, *Client, func()) {
	t.Helper()
	if opts.SocketPath == "" {
		opts.SocketPath = filepath.Join(t.TempDir(), "daemon.sock")
	}
	srv := NewServer(opts)
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	// Poll for readiness — the goroutine has to win the listener race
	// with the test before we dial.
	deadline := time.Now().Add(2 * time.Second)
	var client *Client
	for time.Now().Before(deadline) {
		c, _, err := DialAndHandshake(ctx, opts.SocketPath, "test")
		if err == nil {
			client = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		cancel()
		<-serveErr
		t.Fatal("server never accepted handshake")
	}
	teardown := func() {
		_ = client.Close()
		_ = srv.Stop()
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not shut down in time")
		}
	}
	return srv, client, teardown
}

// TestHandshakeAndStatus: the simplest path. Client connects, hands
// over the version, daemon responds with its version + uptime + pid.
// Validates the JSON-RPC framing end to end.
func TestHandshakeAndStatus(t *testing.T) {
	_, c, teardown := startTestServer(t, ServerOpts{})
	defer teardown()

	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.ServerVersion != Version {
		t.Errorf("server version = %q, want %q", st.ServerVersion, Version)
	}
	if st.UptimeSec < 0 {
		t.Errorf("uptime = %d, want >= 0", st.UptimeSec)
	}
}

// TestToolCallDispatcherWired: the daemon hands tool.call params to
// the user-supplied Dispatcher and returns its result verbatim.
// Phase-1 placeholder before the plugin runtime is wired in Phase 2.
func TestToolCallDispatcherWired(t *testing.T) {
	called := make(chan ToolCallParams, 1)
	disp := func(_ context.Context, p ToolCallParams) (ToolCallResult, error) {
		called <- p
		return ToolCallResult{Content: `{"hello":"world"}`}, nil
	}
	_, c, teardown := startTestServer(t, ServerOpts{Dispatcher: disp})
	defer teardown()

	res, err := c.ToolCall(context.Background(), ToolCallParams{
		Tool:      "fs.read",
		Args:      json.RawMessage(`{"path":"/tmp/x"}`),
		ProjectID: "proj-a",
	})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if res.Content != `{"hello":"world"}` {
		t.Errorf("content = %q", res.Content)
	}
	select {
	case got := <-called:
		if got.Tool != "fs.read" || got.ProjectID != "proj-a" {
			t.Errorf("dispatcher saw %+v", got)
		}
	default:
		t.Fatal("dispatcher not invoked")
	}
}

// TestToolCallAllowList: --tools allowlist enforcement happens server-
// side. A call carrying allow_list=[fs.read] for tool=fs.write must
// be refused with ErrCodeToolDenied — the client can't be trusted to
// enforce its own restrictions when the same daemon serves many.
func TestToolCallAllowList(t *testing.T) {
	disp := func(_ context.Context, _ ToolCallParams) (ToolCallResult, error) {
		t.Fatal("dispatcher should not be invoked when allow_list rejects")
		return ToolCallResult{}, nil
	}
	_, c, teardown := startTestServer(t, ServerOpts{Dispatcher: disp})
	defer teardown()

	_, err := c.ToolCall(context.Background(), ToolCallParams{
		Tool:      "fs.write",
		Args:      json.RawMessage(`{}`),
		ProjectID: "p",
		AllowList: []string{"fs.read"},
	})
	if err == nil {
		t.Fatal("want denial error")
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != ErrCodeToolDenied {
		t.Errorf("err = %v, want ErrCodeToolDenied", err)
	}
}

// TestUnknownMethod: proper JSON-RPC method-not-found path.
func TestUnknownMethod(t *testing.T) {
	_, c, teardown := startTestServer(t, ServerOpts{})
	defer teardown()
	err := c.Call(context.Background(), "no.such.method", nil, nil)
	if err == nil {
		t.Fatal("want method-not-found error")
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != ErrCodeMethodNotFound {
		t.Errorf("err = %v, want ErrCodeMethodNotFound", err)
	}
}

// TestSessionListAndKill: the daemon delegates to the supplied
// callbacks and round-trips the list. KillSession returning false
// (not found) is not an error — kill is idempotent.
func TestSessionListAndKill(t *testing.T) {
	var mu sync.Mutex
	live := []SessionDescriptor{
		{Kind: "pty", ID: 1, Summary: "/bin/bash", Alive: true, ProjectID: "proj-a"},
		{Kind: "pty", ID: 2, Summary: "/bin/cat", Alive: true, ProjectID: "proj-b"},
	}
	listFn := func(projectID string, all bool) []SessionDescriptor {
		mu.Lock()
		defer mu.Unlock()
		if all {
			return append([]SessionDescriptor(nil), live...)
		}
		var out []SessionDescriptor
		for _, s := range live {
			if s.ProjectID == projectID {
				out = append(out, s)
			}
		}
		return out
	}
	killFn := func(p SessionKillParams) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		for i, s := range live {
			if s.Kind == p.Kind && s.ID == p.ID && s.ProjectID == p.ProjectID {
				live = append(live[:i], live[i+1:]...)
				return true, nil
			}
		}
		return false, nil
	}
	_, c, teardown := startTestServer(t, ServerOpts{
		ListSessions: listFn,
		KillSession:  killFn,
	})
	defer teardown()

	// Project scope: proj-a sees only its own session.
	res, err := c.SessionList(context.Background(), SessionListParams{ProjectID: "proj-a"})
	if err != nil {
		t.Fatalf("SessionList: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != 1 {
		t.Errorf("proj-a sessions: %+v", res.Sessions)
	}
	// Kill non-existent session: idempotent OK=false.
	kr, err := c.SessionKill(context.Background(), SessionKillParams{Kind: "pty", ID: 99, ProjectID: "proj-a"})
	if err != nil {
		t.Fatalf("kill missing: %v", err)
	}
	if kr.OK {
		t.Errorf("kill missing: OK should be false")
	}
	// Kill real one.
	kr, err = c.SessionKill(context.Background(), SessionKillParams{Kind: "pty", ID: 1, ProjectID: "proj-a"})
	if err != nil {
		t.Fatalf("kill real: %v", err)
	}
	if !kr.OK {
		t.Errorf("kill real: OK should be true")
	}
}

// TestShutdownClosesServer: daemon.shutdown causes Stop, the listener
// closes, and the server's Done channel fires.
func TestShutdownClosesServer(t *testing.T) {
	srv, c, _ := startTestServer(t, ServerOpts{})
	if err := c.Shutdown(context.Background(), false, "test"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-srv.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after shutdown call")
	}
	_ = c.Close()
}

// TestVersionSkew: a client speaking an older protocol version than
// MinClientVersion is rejected with ErrCodeVersionSkew before any
// dispatch happens. Confirms the daemon can refuse to serve a
// down-revved CLI cleanly.
func TestVersionSkew(t *testing.T) {
	_, c, teardown := startTestServer(t, ServerOpts{})
	defer teardown()

	// Send a hand-rolled handshake with an older version.
	var res HandshakeResult
	err := c.Call(context.Background(), MethodHandshake, HandshakeParams{
		ClientVersion: "0", // pre-v1 hypothetical
		ClientName:    "test",
	}, &res)
	if err == nil {
		t.Fatal("want version-skew error, got nil")
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != ErrCodeVersionSkew {
		t.Errorf("err = %v, want ErrCodeVersionSkew", err)
	}
}

// TestIdleExitState: shouldIdleExit returns true iff
// (now - lastActivity) >= IdleTimeout AND no live sessions AND no
// inflight calls. We exercise the predicate directly rather than
// driving the actual minute-resolution ticker — too slow for unit
// tests, and the predicate is the load-bearing piece of the policy.
func TestIdleExitState(t *testing.T) {
	live := false
	srv := NewServer(ServerOpts{
		SocketPath:  "/tmp/stado-idle-test-not-bound.sock",
		IdleTimeout: 1 * time.Millisecond,
		ListSessions: func(_ string, _ bool) []SessionDescriptor {
			if !live {
				return nil
			}
			return []SessionDescriptor{{Kind: "pty", ID: 1, Alive: true}}
		},
	})
	// Force-set last activity to long ago so timeout has elapsed.
	srv.lastActivityNanos.Store(time.Now().Add(-time.Hour).UnixNano())

	if !srv.shouldIdleExit() {
		t.Errorf("with no live sessions + zero inflight: want exit=true")
	}
	live = true
	if srv.shouldIdleExit() {
		t.Errorf("with a live session: want exit=false")
	}
	live = false
	srv.mu.Lock()
	srv.inflight = 1
	srv.mu.Unlock()
	if srv.shouldIdleExit() {
		t.Errorf("with inflight=1: want exit=false")
	}
}

// TestProjectScopedSessionList: ProjectID="" + all=false returns the
// unscoped scope only; ProjectID="X" returns only project X's; all=true
// returns everything. Validates the listSessions contract the daemon
// docs.
func TestProjectScopedSessionList(t *testing.T) {
	state := []SessionDescriptor{
		{Kind: "pty", ID: 1, Alive: true, ProjectID: "a"},
		{Kind: "pty", ID: 1, Alive: true, ProjectID: "b"},
		{Kind: "pty", ID: 5, Alive: true, ProjectID: ""},
	}
	listFn := func(projectID string, all bool) []SessionDescriptor {
		out := make([]SessionDescriptor, 0)
		for _, s := range state {
			if all {
				out = append(out, s)
			} else if s.ProjectID == projectID {
				out = append(out, s)
			}
		}
		return out
	}
	_, c, teardown := startTestServer(t, ServerOpts{ListSessions: listFn})
	defer teardown()

	type tc struct {
		name      string
		params    SessionListParams
		wantCount int
	}
	cases := []tc{
		{"empty (unscoped scope)", SessionListParams{}, 1},
		{"project a", SessionListParams{ProjectID: "a"}, 1},
		{"project b", SessionListParams{ProjectID: "b"}, 1},
		{"all", SessionListParams{AllProjects: true}, 3},
	}
	for _, c2 := range cases {
		t.Run(c2.name, func(t *testing.T) {
			res, err := c.SessionList(context.Background(), c2.params)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(res.Sessions) != c2.wantCount {
				t.Errorf("len = %d, want %d (got %+v)", len(res.Sessions), c2.wantCount, res.Sessions)
			}
		})
	}
}

// TestRemoveStaleSocket: with no daemon listening, the socket file is
// removed. With one listening, ErrSocketInUse is returned.
func TestRemoveStaleSocket(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "daemon.sock")

	// Stale (no listener): treat as remove-success when file exists,
	// no-op when it doesn't.
	removed, err := RemoveStaleSocket(sock)
	if err != nil || removed {
		t.Fatalf("missing socket: removed=%v err=%v want false,nil", removed, err)
	}

	// Live daemon: in-use error.
	srv, c, teardown := startTestServer(t, ServerOpts{SocketPath: sock})
	defer teardown()
	_, err = RemoveStaleSocket(sock)
	if !errors.Is(err, ErrSocketInUse) {
		t.Fatalf("live socket: want ErrSocketInUse, got %v", err)
	}
	_ = c.Close()
	_ = srv.Stop()
	select {
	case <-srv.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}

	// Now the file is gone (Stop unlinks); RemoveStaleSocket reports
	// no-op.
	removed, err = RemoveStaleSocket(sock)
	if err != nil || removed {
		t.Errorf("after Stop: removed=%v err=%v", removed, err)
	}
}
