package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// isLikelyGoTestBinary returns true when path looks like a Go test
// binary: ends in ".test" (Go's `go test -c` output convention),
// lives under a `/go-build` cache directory, or contains the
// transient `go-buildNNN` segment that `go test` uses for its
// staged binaries. Conservative — false-positives are fine; the
// only consequence is forcing the operator/test to set
// STADO_DAEMON=off explicitly.
func isLikelyGoTestBinary(path string) bool {
	if path == "" {
		return false
	}
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	if strings.HasSuffix(base, ".test") {
		return true
	}
	if strings.Contains(path, "/go-build") {
		return true
	}
	return false
}

// Client is a stateless wrapper around a UDS connection to the daemon.
// One Client maps to one connection; safe to share across goroutines —
// requests are serialised through a per-conn write mutex and responses
// are routed back to the matching call by JSON-RPC ID.
//
// Stateless on the wire is deliberate: the daemon authoritatively holds
// session state by (project_id, session_id), not by connection identity.
// Closing and reopening a Client doesn't lose anything; the daemon
// process is the single source of truth.
type Client struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[uint64]chan *Response
	nextID    atomic.Uint64
	closed    atomic.Bool

	readErr atomic.Value // error from the read loop after it exits
}

// Dial opens a connection to the daemon at socketPath. The connection
// is kept open until Close is called; concurrent calls are multiplexed
// over it. Use DialAndHandshake when you also want the version check
// in one round-trip.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("daemon: dial %s: %w", socketPath, err)
	}
	c := &Client{
		conn:    conn,
		br:      bufio.NewReaderSize(conn, 64<<10),
		pending: make(map[uint64]chan *Response),
	}
	go c.readLoop()
	return c, nil
}

// DialAndHandshake opens a connection AND performs daemon.handshake in
// one shot. Returns a ready-to-use Client; on version mismatch the
// connection is closed and the error wraps ErrCodeVersionSkew so callers
// can decide whether to fall back to single-shot mode.
func DialAndHandshake(ctx context.Context, socketPath, clientName string) (*Client, *HandshakeResult, error) {
	c, err := Dial(ctx, socketPath)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.Handshake(ctx, clientName)
	if err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	return c, res, nil
}

// Close shuts down the connection and unblocks any in-flight callers.
// Idempotent.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	err := c.conn.Close()
	// Wake any waiters with a deterministic error so they don't hang.
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	return err
}

// Call sends a method+params and blocks for the response. The supplied
// ctx cancellation aborts the call from the client's perspective; the
// daemon may still complete the work (no out-of-band cancellation in v1).
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	idJSON, _ := json.Marshal(id)
	var paramsJSON json.RawMessage
	if params != nil {
		buf, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("daemon client: marshal params: %w", err)
		}
		paramsJSON = buf
	}
	req := Request{
		JSONRPC: "2.0",
		ID:      idJSON,
		Method:  method,
		Params:  paramsJSON,
	}
	wire, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("daemon client: marshal req: %w", err)
	}
	wire = append(wire, '\n')

	respCh := make(chan *Response, 1)
	c.pendingMu.Lock()
	if c.closed.Load() {
		c.pendingMu.Unlock()
		return errors.New("daemon client: closed")
	}
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	c.writeMu.Lock()
	_, werr := c.conn.Write(wire)
	c.writeMu.Unlock()
	if werr != nil {
		return fmt.Errorf("daemon client: write: %w", werr)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			// Channel closed by Close() before a response came back.
			if v, _ := c.readErr.Load().(error); v != nil {
				return fmt.Errorf("daemon client: connection closed: %w", v)
			}
			return errors.New("daemon client: connection closed before response")
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("daemon client: unmarshal result: %w", err)
			}
		}
		return nil
	}
}

func (c *Client) readLoop() {
	for {
		line, err := c.br.ReadBytes('\n')
		if err != nil {
			c.readErr.Store(err)
			c.pendingMu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			return
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// Malformed frame — drop and keep reading. The client
			// caller will eventually time out on its select.
			continue
		}
		var idNum uint64
		if err := json.Unmarshal(resp.ID, &idNum); err != nil {
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[idNum]
		c.pendingMu.Unlock()
		if !ok {
			// No pending caller — likely a race with Close. Drop.
			continue
		}
		ch <- &resp
	}
}

// Handshake performs daemon.handshake and returns the daemon's metadata.
func (c *Client) Handshake(ctx context.Context, clientName string) (*HandshakeResult, error) {
	var res HandshakeResult
	if err := c.Call(ctx, MethodHandshake, HandshakeParams{
		ClientVersion: Version,
		ClientName:    clientName,
	}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Status fetches daemon.status.
func (c *Client) Status(ctx context.Context) (*StatusResult, error) {
	var res StatusResult
	if err := c.Call(ctx, MethodStatus, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Shutdown asks the daemon to exit. Returns nil on accepted shutdown.
// Caller should expect the connection to close shortly after.
func (c *Client) Shutdown(ctx context.Context, force bool, reason string) error {
	var res ShutdownResult
	return c.Call(ctx, MethodShutdown, ShutdownParams{Force: force, Reason: reason}, &res)
}

// ToolCall dispatches a tool. Returns the daemon's ToolCallResult — the
// caller decides how to render Content/Error to its own output.
func (c *Client) ToolCall(ctx context.Context, params ToolCallParams) (*ToolCallResult, error) {
	var res ToolCallResult
	if err := c.Call(ctx, MethodToolCall, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ToolList queries the daemon's tool catalogue.
func (c *Client) ToolList(ctx context.Context) (*ToolListResult, error) {
	var res ToolListResult
	if err := c.Call(ctx, MethodToolList, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SessionList queries live sessions, optionally scoped to one project.
func (c *Client) SessionList(ctx context.Context, params SessionListParams) (*SessionListResult, error) {
	var res SessionListResult
	if err := c.Call(ctx, MethodSessionList, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SessionKill destroys a session by (kind, id, project).
func (c *Client) SessionKill(ctx context.Context, params SessionKillParams) (*SessionKillResult, error) {
	var res SessionKillResult
	if err := c.Call(ctx, MethodSessionKill, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// EnsureRunning either confirms a daemon is listening at socketPath or
// auto-spawns one (running `stadoBinary daemon start --quiet`) and waits
// up to maxWait for the socket to come up. Returns a ready Client on
// success; falls through to error after the timeout.
//
// The auto-spawn path forks a detached child process — the daemon
// continues running after this function (and the calling `stado tool run`)
// exits. Pid 1 reaps it on session end; XDG_RUNTIME_DIR/tmpfs cleanup
// removes the socket on logout.
//
// Defensive: refuses to auto-spawn when the host binary looks like a
// Go test binary (`*.test` or under `/tmp/go-build*`). Without this
// check, a test that exercises a PTY-bound CLI path would invoke this
// function with stadoBinary == os.Executable() == the test binary,
// which when re-run as a "daemon" silently re-runs the whole test
// suite. Each test that triggers this re-runs forks again — fork
// bomb. The single OOM that surfaced this on 2026-05-08 left 351
// stado.test processes consuming ~150 GiB virtual before the kernel
// killed Chrome / Claude / gopls. This guard makes test code that
// forgot `STADO_DAEMON=off` fail loudly with an actionable error
// instead of detonating.
func EnsureRunning(ctx context.Context, socketPath, stadoBinary string, maxWait time.Duration) (*Client, *HandshakeResult, error) {
	// Fast path: already up.
	if c, h, err := DialAndHandshake(ctx, socketPath, "stado-tool-run"); err == nil {
		return c, h, nil
	}
	if isLikelyGoTestBinary(stadoBinary) {
		return nil, nil, fmt.Errorf("daemon auto-spawn refused: host binary %q looks like a Go test binary; spawning it as a daemon would re-run the test suite. Tests that exercise PTY-bound tools must set STADO_DAEMON=off (or wire a real stado binary)", stadoBinary)
	}
	// Try to clear a stale socket before spawning, otherwise the new
	// daemon will refuse to bind.
	if _, err := RemoveStaleSocket(socketPath); err != nil && !errors.Is(err, ErrSocketInUse) {
		return nil, nil, err
	}
	// Auto-spawn. Detached so the parent's exit doesn't take it down.
	cmd := exec.Command(stadoBinary, "daemon", "start", "--quiet")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachAttr() // platform-specific in autospawn_*.go
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("daemon auto-spawn: %w", err)
	}
	// Don't Wait — parent should not block on the daemon process.
	go func() { _ = cmd.Process.Release() }()

	deadline := time.Now().Add(maxWait)
	backoff := 25 * time.Millisecond
	for {
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("daemon auto-spawn: socket %s did not appear within %s", socketPath, maxWait)
		}
		if c, h, err := DialAndHandshake(ctx, socketPath, "stado-tool-run"); err == nil {
			return c, h, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 200*time.Millisecond {
			backoff *= 2
		}
	}
}
