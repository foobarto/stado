package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/version"
)

// stadoVersion returns the build-stamped semver. Wrapped here so the
// dispatch handlers don't import the version package directly (keeps
// the daemon's import surface narrow + makes testing/stubbing easy).
func stadoVersion() string { return version.Version }

// ServerOpts configures a daemon instance. Zero values pick reasonable
// defaults — Logger=io.Discard, IdleTimeout=DefaultIdleTimeout, etc.
type ServerOpts struct {
	// SocketPath is the absolute path of the UDS to bind. Caller is
	// responsible for clearing stale sockets before starting (see
	// RemoveStaleSocket); the server returns an error if the path is
	// already in use.
	SocketPath string

	// IdleTimeout, when > 0, exits the daemon after this much wall-clock
	// time with zero live sessions and zero in-flight calls. 0 disables
	// the timeout (operator must `stado daemon stop`).
	IdleTimeout time.Duration

	// Logger receives one line per request + lifecycle event. Pass
	// io.Discard to silence; a real *os.File or io.MultiWriter for
	// observability. nil = io.Discard.
	Logger io.Writer

	// Dispatcher is the function that handles tool.call params and
	// returns a result. Phase 1 wires a stub that returns "not yet
	// implemented"; Phase 2 wires the real plugin runtime.
	Dispatcher Dispatcher

	// ListSessions returns the currently-live sessions across the
	// optional project filter. Phase 1 returns an empty slice; Phase 2
	// reports pty.Manager state.
	ListSessions func(projectID string, all bool) []SessionDescriptor

	// KillSession destroys a session by (kind, id, project_id). Returns
	// (true, nil) when destroyed, (false, nil) when not found,
	// (false, err) on failure.
	KillSession func(params SessionKillParams) (bool, error)

	// ListTools returns the daemon's current tool catalogue. Phase 1
	// returns an empty slice; Phase 2 wires BuildRegistryWithPlugins.
	ListTools func() []ToolDescriptor

	// BrokerDispatcher, when set, handles JSON-RPC methods under
	// the broker.v1.* namespace (MethodBrokerPrefix). The caller
	// constructs this closure from internal/broker.Service.Dispatch
	// + an error-to-Error bridge; see cmd/stado/daemon.go.
	//
	// When nil, broker.v1.* methods return ErrCodeMethodNotFound —
	// preserves backward compatibility for tests and pre-v1 callers
	// that haven't built a broker service yet.
	BrokerDispatcher BrokerDispatcher
}

// Dispatcher dispatches a tool.call to the plugin runtime. Implementations
// in Phase 2 instantiate the wasm plugin against the daemon's shared host
// (so PTY sessions persist) and return the rendered tool.Result content.
type Dispatcher func(ctx context.Context, params ToolCallParams) (ToolCallResult, error)

// BrokerDispatcher is the function signature ServerOpts uses to
// route broker.v1.* JSON-RPC method calls. Returns the marshalled
// success result on admit, or a non-nil *Error with the
// JSON-RPC-shaped code+message on deny / validation failure /
// internal error.
//
// The signature is deliberately decoupled from internal/broker so
// internal/daemon does not depend on internal/broker (avoids
// upward dependency from the protocol layer to the policy layer).
type BrokerDispatcher func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *Error)

// Server is a running daemon. Construct with NewServer; drive with
// Serve; stop with Stop.
type Server struct {
	opts      ServerOpts
	logger    io.Writer
	startedAt time.Time

	listener net.Listener

	mu       sync.Mutex
	closing  bool
	closed   chan struct{}
	conns    map[net.Conn]struct{}
	inflight int

	// idle tracking — wall clock of the last-completed call. Idle loop
	// reads this without locking via atomic to avoid serialising every
	// request behind a mutex.
	lastActivityNanos atomic.Int64
	totalCalls        atomic.Uint64
}

// NewServer prepares a daemon. The socket isn't bound until Serve is
// called — NewServer is cheap and safe to call from tests.
func NewServer(opts ServerOpts) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = io.Discard
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	s := &Server{
		opts:      opts,
		logger:    logger,
		startedAt: time.Now(),
		closed:    make(chan struct{}),
		conns:     make(map[net.Conn]struct{}),
	}
	s.lastActivityNanos.Store(time.Now().UnixNano())
	return s
}

// Serve binds the UDS socket and accepts connections until ctx is done
// or Stop is called. Returns the first non-recoverable error; ctx
// cancellation returns nil. Idempotent re-Serve is not supported — once
// the listener is closed the Server is done.
func (s *Server) Serve(ctx context.Context) error {
	if err := EnsureSocketDir(s.opts.SocketPath); err != nil {
		return err
	}
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "unix", s.opts.SocketPath)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", s.opts.SocketPath, err)
	}
	// Tighten the socket permissions immediately. Unix sockets created
	// via Listen honour umask; we want unconditional 0700 so a wide
	// umask doesn't accidentally publish the socket to the local user
	// group / others.
	if err := os.Chmod(s.opts.SocketPath, 0o700); err != nil {
		_ = l.Close()
		return fmt.Errorf("daemon: chmod socket: %w", err)
	}
	s.mu.Lock()
	s.listener = l
	s.mu.Unlock()

	s.logf("daemon: listening on %s (pid=%d)", s.opts.SocketPath, os.Getpid())

	go s.idleLoop(ctx)

	// Accept loop. Closes when listener is closed (Stop) or ctx done.
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		// Wake on EITHER a cancelled context (SIGINT/SIGTERM or parent
		// cancel) or a direct Stop() — handleShutdown and the idle-timeout
		// reaper close s.closed without touching ctx. Without the s.closed
		// arm this goroutine parks on ctx.Done() forever, so the accept
		// loop's <-acceptDone never returns and Serve() (hence the daemon
		// process) leaks even though `daemon stop` reported success. Stop()
		// is idempotent, so calling it again on the ctx path is harmless.
		select {
		case <-ctx.Done():
		case <-s.closed:
		}
		_ = s.Stop()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if s.isClosing() || errors.Is(err, net.ErrClosed) {
				<-acceptDone
				return nil
			}
			s.logf("daemon: accept error: %v", err)
			continue
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go s.handleConn(ctx, c)
	}
}

// Stop closes the listener, refuses new connections, removes the socket
// file, and signals connected handlers to wind down. In-flight calls
// finish; their goroutines exit when their connection closes. Idempotent.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	l := s.listener
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	if l != nil {
		_ = l.Close()
	}
	// Closing client connections doesn't yank in-flight tool calls
	// in-process (those have their own ctx the dispatcher may or may
	// not honour); it just prevents new requests on the socket. Real
	// graceful drain is in Phase 2 once Dispatcher is wired with a
	// cancellable context per call.
	for _, c := range conns {
		_ = c.Close()
	}
	if s.opts.SocketPath != "" {
		_ = os.Remove(s.opts.SocketPath)
	}
	close(s.closed)
	s.logf("daemon: stopped")
	return nil
}

// Done returns a channel that's closed when the daemon has fully
// stopped. Tests Serve in a goroutine and use this to wait for clean
// shutdown.
func (s *Server) Done() <-chan struct{} { return s.closed }

func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// handleConn reads newline-delimited JSON-RPC requests from c and writes
// responses back. Connection-level state is intentionally minimal — the
// daemon authoritatively holds session state by (project_id, session_id),
// not by connection identity, so a client can reconnect freely without
// losing context.
func (s *Server) handleConn(ctx context.Context, c net.Conn) {
	defer func() {
		_ = c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()

	// SO_PEERCRED uid check (linux). Matching uid is an authentication
	// signal: filesystem perms restrict the socket to mode 0700, but a
	// belt-and-braces uid check defends against a same-user namespace
	// confusion or a debug build that loosened the permissions.
	if err := checkPeerUID(c); err != nil {
		s.logf("daemon: reject connection: %v", err)
		return
	}

	// Per-connection deadline for the handshake — if the client doesn't
	// send daemon.handshake within HandshakeTimeout we close. This is
	// short and only on the first read; subsequent reads have no
	// deadline (callers can hold connections open).
	_ = c.SetReadDeadline(time.Now().Add(HandshakeTimeout))
	br := bufio.NewReaderSize(c, 64<<10)

	first := true
	for {
		// Codex C6/N-b P2 — read frames in chunks bounded by the buffer
		// size, accumulating up to MaxRequestBytes. Pre-fix used
		// `br.ReadBytes('\n')` which buffers the ENTIRE line in memory
		// before returning, then checked the size cap after. A same-UID
		// peer sending a 4 GiB line forced the daemon to allocate 4 GiB
		// just to refuse it. The chunked approach below caps the live
		// allocation at MaxRequestBytes and drains the rest of any
		// over-cap line so the connection can recover for the next
		// frame.
		line, err := readFramedLine(br, MaxRequestBytes)
		if err != nil {
			if errors.Is(err, errFrameTooLarge) {
				// readFramedLine already advanced the reader past the
				// oversized frame's '\n' (drain-as-you-go); just refuse
				// + continue reading subsequent frames.
				s.writeErr(c, nil, ErrCodeInvalidRequest, "request too large")
				continue
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.logf("daemon: read: %v", err)
			}
			return
		}
		if first {
			_ = c.SetReadDeadline(time.Time{}) // clear deadline after handshake
			first = false
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeErr(c, nil, ErrCodeParse, "parse error: "+err.Error())
			continue
		}
		if req.JSONRPC != "2.0" {
			s.writeErr(c, req.ID, ErrCodeInvalidRequest, "jsonrpc field must be \"2.0\"")
			continue
		}
		s.dispatch(ctx, c, &req)
	}
}

// errFrameTooLarge is the sentinel readFramedLine returns when the
// accumulated bytes for a frame exceed the per-frame cap. The reader
// is advanced past the frame regardless (drain-as-you-go), so the
// connection can resync to the next frame without separate drain
// machinery on the caller side.
var errFrameTooLarge = errors.New("daemon: frame exceeds MaxRequestBytes")

// readFramedLine reads bytes from br until '\n', refusing to allocate
// past maxBytes. Returns the bytes including the trailing '\n' on
// success. Returns errFrameTooLarge when the frame's total length
// exceeds maxBytes; in that case the reader is still advanced past
// the frame's '\n' (or to EOF) so the caller can resume reading
// subsequent valid frames without additional drain logic.
//
// Codex C6/N-b P2: replaces the prior `bufio.Reader.ReadBytes('\n')`
// which buffered the entire line (up to arbitrary size) BEFORE the
// MaxRequestBytes check. ReadSlice returns bufio's internal buffer
// without copying, so the per-call working set stays bounded by
// bufio's buffer size (64 KiB at construction); the accumulator only
// grows while under cap. Past cap we keep iterating ReadSlice (to
// consume the rest of the oversized line) but stop appending so the
// allocation stays at maxBytes regardless of the peer's actual frame
// size.
func readFramedLine(br *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 && !over {
			if len(buf)+len(chunk) > maxBytes {
				// Past cap: stop appending. We keep looping so the
				// reader advances past the trailing '\n' (drain-as-
				// you-go). The cap is on the *accumulator* allocation;
				// bufio's internal buffer is bounded separately by
				// the buffer size set at NewReaderSize.
				over = true
			} else {
				// append copies chunk's bytes into buf with amortized
				// growth — ReadSlice's slice is only valid until the
				// next read, so the copy is mandatory; using append
				// makes the total work O(n) instead of O(n²). Pre-fix
				// used `make + two copies` per chunk: at 64 KiB
				// ReadSlice chunks, a near-32 MiB valid request copied
				// gigabytes redundantly, letting a local client induce
				// high CPU even under cap (Codex P2 review #65 caught
				// this in my prior commit).
				buf = append(buf, chunk...)
			}
		}
		if err == nil {
			// Delimiter found. Frame complete.
			if over {
				return nil, errFrameTooLarge
			}
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			// No delimiter in current buffer fill; keep reading.
			continue
		}
		// EOF / I/O error before any '\n'. If we were over cap we
		// surface that; otherwise propagate whatever the reader gave
		// us (typically io.EOF on clean disconnect).
		if over {
			return nil, errFrameTooLarge
		}
		return nil, err
	}
}

func (s *Server) dispatch(ctx context.Context, c net.Conn, req *Request) {
	// Broker.v1.* methods are routed to the optional BrokerDispatcher.
	// The prefix check happens before the switch so the broker can
	// own its own sub-routing (session.create vs session.terminate
	// vs ...) without each method needing a case here.
	if strings.HasPrefix(req.Method, MethodBrokerPrefix) {
		s.handleBrokerDispatch(ctx, c, req)
		return
	}
	switch req.Method {
	case MethodHandshake:
		s.handleHandshake(c, req)
	case MethodStatus:
		s.handleStatus(c, req)
	case MethodShutdown:
		s.handleShutdown(c, req)
	case MethodToolCall:
		s.handleToolCall(ctx, c, req)
	case MethodToolList:
		s.handleToolList(c, req)
	case MethodSessionList:
		s.handleSessionList(c, req)
	case MethodSessionKill:
		s.handleSessionKill(c, req)
	default:
		s.writeErr(c, req.ID, ErrCodeMethodNotFound, "unknown method: "+req.Method)
	}
}

// handleBrokerDispatch routes a broker.v1.* JSON-RPC method call to
// ServerOpts.BrokerDispatcher, or returns ErrCodeMethodNotFound if
// no broker dispatcher has been configured.
func (s *Server) handleBrokerDispatch(ctx context.Context, c net.Conn, req *Request) {
	if s.opts.BrokerDispatcher == nil {
		s.writeErr(c, req.ID, ErrCodeMethodNotFound, "broker not configured: "+req.Method)
		return
	}
	result, brokerErr := s.opts.BrokerDispatcher(ctx, req.Method, req.Params)
	if brokerErr != nil {
		s.writeErr(c, req.ID, brokerErr.Code, brokerErr.Message)
		return
	}
	s.writeResult(c, req.ID, result)
}

func (s *Server) handleHandshake(c net.Conn, req *Request) {
	var p HandshakeParams
	if err := json.Unmarshal(req.Params, &p); err != nil && len(req.Params) > 0 {
		s.writeErr(c, req.ID, ErrCodeInvalidParams, err.Error())
		return
	}
	if p.ClientVersion != "" && p.ClientVersion < MinClientVersion {
		s.writeErr(c, req.ID, ErrCodeVersionSkew,
			fmt.Sprintf("client speaks v%s; daemon requires v%s+", p.ClientVersion, MinClientVersion))
		return
	}
	res := HandshakeResult{
		ServerVersion: Version,
		DaemonPID:     os.Getpid(),
		StadoVersion:  stadoVersion(),
	}
	s.writeResult(c, req.ID, res)
}

func (s *Server) handleStatus(c net.Conn, req *Request) {
	live := 0
	if s.opts.ListSessions != nil {
		for _, sd := range s.opts.ListSessions("", true) {
			if sd.Alive {
				live++
			}
		}
	}
	idle := time.Now().UnixNano() - s.lastActivityNanos.Load()
	if idle < 0 {
		idle = 0
	}
	res := StatusResult{
		ServerVersion:  Version,
		StadoVersion:   stadoVersion(),
		StartedAt:      s.startedAt,
		UptimeSec:      int64(time.Since(s.startedAt).Seconds()),
		DaemonPID:      os.Getpid(),
		SocketPath:     s.opts.SocketPath,
		LiveSessions:   live,
		TotalCalls:     s.totalCalls.Load(),
		IdleSec:        int64(time.Duration(idle).Seconds()),
		IdleTimeoutSec: int64(s.opts.IdleTimeout.Seconds()),
	}
	s.writeResult(c, req.ID, res)
}

func (s *Server) handleShutdown(c net.Conn, req *Request) {
	var p ShutdownParams
	_ = json.Unmarshal(req.Params, &p)
	s.logf("daemon: shutdown requested (force=%v reason=%q)", p.Force, p.Reason)
	s.writeResult(c, req.ID, ShutdownResult{OK: true})
	// Stop on a separate goroutine so the response is flushed first.
	go func() { _ = s.Stop() }()
}

func (s *Server) handleToolCall(ctx context.Context, c net.Conn, req *Request) {
	var p ToolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeErr(c, req.ID, ErrCodeInvalidParams, err.Error())
		return
	}
	if p.Tool == "" {
		s.writeErr(c, req.ID, ErrCodeInvalidParams, "tool field required")
		return
	}
	if s.opts.Dispatcher == nil {
		s.writeErr(c, req.ID, ErrCodeInternal, "daemon: dispatcher not wired")
		return
	}
	if len(p.AllowList) > 0 && !toolInAllowList(p.Tool, p.AllowList) {
		s.writeErr(c, req.ID, ErrCodeToolDenied,
			fmt.Sprintf("tool %q not in --tools allow list (call carried %d entries)", p.Tool, len(p.AllowList)))
		return
	}
	s.mu.Lock()
	s.inflight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		s.mu.Unlock()
		s.lastActivityNanos.Store(time.Now().UnixNano())
		s.totalCalls.Add(1)
	}()
	callCtx := ctx
	if p.TimeoutMs > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, time.Duration(p.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	res, err := s.opts.Dispatcher(callCtx, p)
	if err != nil {
		s.writeErr(c, req.ID, ErrCodeInternal, err.Error())
		return
	}
	s.writeResult(c, req.ID, res)
}

func (s *Server) handleToolList(c net.Conn, req *Request) {
	var tools []ToolDescriptor
	if s.opts.ListTools != nil {
		tools = s.opts.ListTools()
	}
	s.writeResult(c, req.ID, ToolListResult{Tools: tools})
}

func (s *Server) handleSessionList(c net.Conn, req *Request) {
	var p SessionListParams
	_ = json.Unmarshal(req.Params, &p)
	var sessions []SessionDescriptor
	if s.opts.ListSessions != nil {
		sessions = s.opts.ListSessions(p.ProjectID, p.AllProjects)
	}
	s.writeResult(c, req.ID, SessionListResult{Sessions: sessions})
}

func (s *Server) handleSessionKill(c net.Conn, req *Request) {
	var p SessionKillParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeErr(c, req.ID, ErrCodeInvalidParams, err.Error())
		return
	}
	if s.opts.KillSession == nil {
		s.writeResult(c, req.ID, SessionKillResult{OK: false})
		return
	}
	ok, err := s.opts.KillSession(p)
	if err != nil {
		s.writeErr(c, req.ID, ErrCodeInternal, err.Error())
		return
	}
	s.writeResult(c, req.ID, SessionKillResult{OK: ok})
}

func (s *Server) writeResult(c net.Conn, id json.RawMessage, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.writeErr(c, id, ErrCodeInternal, "marshal result: "+err.Error())
		return
	}
	resp := Response{JSONRPC: "2.0", ID: id, Result: body}
	out, _ := json.Marshal(resp)
	out = append(out, '\n')
	if _, err := c.Write(out); err != nil {
		s.logf("daemon: write: %v", err)
	}
}

func (s *Server) writeErr(c net.Conn, id json.RawMessage, code int, msg string) {
	resp := Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: msg}}
	out, _ := json.Marshal(resp)
	out = append(out, '\n')
	if _, err := c.Write(out); err != nil {
		s.logf("daemon: write err: %v", err)
	}
}

// idleLoop polls IdleTimeout and stops the daemon when the
// last-activity timestamp is older than the threshold AND there are
// zero live sessions. The "zero sessions" gate matters: a daemon
// holding a long-lived shell.spawn shouldn't exit just because no tool
// calls have arrived in a while — the operator may still be running a
// long PTY job.
func (s *Server) idleLoop(ctx context.Context) {
	if s.opts.IdleTimeout <= 0 {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case <-tick.C:
			if s.shouldIdleExit() {
				s.logf("daemon: idle timeout (no calls for %s, no live sessions); exiting", s.opts.IdleTimeout)
				_ = s.Stop()
				return
			}
		}
	}
}

func (s *Server) shouldIdleExit() bool {
	last := s.lastActivityNanos.Load()
	if time.Since(time.Unix(0, last)) < s.opts.IdleTimeout {
		return false
	}
	s.mu.Lock()
	if s.inflight > 0 {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	if s.opts.ListSessions != nil {
		for _, sd := range s.opts.ListSessions("", true) {
			if sd.Alive {
				return false
			}
		}
	}
	return true
}

func (s *Server) logf(format string, args ...any) {
	fmt.Fprintf(s.logger, time.Now().Format(time.RFC3339)+" "+format+"\n", args...)
}

// toolInAllowList reports whether tool matches any glob pattern in
// allow. Codex G7/I-b P1 — pre-fix this used `a == tool` exact-string
// match, so `[tools].enabled=["shell.*"]` silently failed to match
// `shell__bash` (the wire form): the operator's intended allowlist
// was effectively a denylist for every tool. Routed through the
// canonical runtime.ToolMatchesGlob so all daemon dispatch surfaces
// share one matcher with the rest of stado.
func toolInAllowList(tool string, allow []string) bool {
	for _, a := range allow {
		if runtime.ToolMatchesGlob(tool, a) {
			return true
		}
	}
	return false
}
