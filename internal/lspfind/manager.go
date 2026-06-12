package lspfind

import (
	"context"
	"sync"

	"github.com/foobarto/stado/internal/lsp"
)

// LSPClientManager owns the lifetime of LSP server processes for one
// session. It caches at most one live client per (workdir,
// languageServer) tuple, amortising the (expensive) initialize
// handshake across every tool call in the session, and reaps every
// server on CloseAll() so a torn-down session never leaks a gopls /
// rust-analyzer / pyright child.
//
// Thread-safety: a sync.RWMutex guards the cache so a future TUI render
// goroutine can read the live-client set (e.g. to surface diagnostics)
// concurrently with the agent dispatching tool calls. The read path
// (fast: a cache hit on a live client) takes only the read lock;
// launching a new server or evicting a dead one escalates to the write
// lock. Launching itself happens *outside* any lock so a slow server
// start (gopls indexing a large module) doesn't stall a concurrent
// reader.
//
// Crash detection: lsp.Client.Alive() flips false when the server's
// read loop exits (EOF / crash / Close). ClientFor checks it on the
// cache-hit path; a dead client is dropped and the next call lazily
// relaunches a fresh server for the tuple.
//
// ctx: the manager pins the session context at construction. Every
// launched server is rooted at it (lsp.Launch ties the child's
// exec.CommandContext to ctx), so cancelling the session context kills
// the servers even if CloseAll is somehow missed. CloseAll remains the
// orderly path (shutdown/exit handshake before kill).
type LSPClientManager struct {
	ctx     context.Context
	mu      sync.RWMutex
	clients map[string]*lsp.Client // "<workdir>|<server>" → live client
	closed  bool
}

// NewLSPClientManager returns a session-scoped manager. ctx is the
// session context: launched servers inherit it, so session cancellation
// tears the servers down even without an explicit CloseAll. A nil ctx
// is treated as context.Background().
func NewLSPClientManager(ctx context.Context) *LSPClientManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &LSPClientManager{
		ctx:     ctx,
		clients: map[string]*lsp.Client{},
	}
}

func clientKey(workdir, server string) string { return workdir + "|" + server }

// ClientFor returns a live LSP client for the (workdir, server) tuple,
// launching+initialising one on first use and reusing it thereafter.
// A cached client that has died (server EOF / crash) is evicted and a
// fresh one launched. Concurrent callers requesting the same tuple
// share the resulting client; the launch races but at most one client
// survives in the cache (a loser closes its extra handle).
//
// The launched server process is rooted at the manager's session ctx —
// NOT the per-call ctx — so the client (and its expensive initialize
// state) outlives the individual tool call and dies with the session.
// The per-call ctx still scopes the subsequent query (Definition /
// Hover / …) the caller issues against the returned client.
func (m *LSPClientManager) ClientFor(_ context.Context, workdir, server string) (*lsp.Client, error) {
	key := clientKey(workdir, server)

	// Fast path: read-locked cache hit on a live client.
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil, errManagerClosed
	}
	if c, ok := m.clients[key]; ok && c.Alive() {
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()

	// Slow path: launch a new server outside the lock so a slow start
	// (e.g. gopls indexing a large module) doesn't block concurrent
	// readers. Root the child at the session ctx so the server outlives
	// this call and is reaped when the session is cancelled.
	c, err := launch(m.ctx, server, workdir)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		_ = c.Close()
		return nil, errManagerClosed
	}
	// Re-check under the write lock: another goroutine may have won the
	// race and cached a live client while we were launching. If so, keep
	// theirs and close ours.
	if existing, ok := m.clients[key]; ok && existing.Alive() {
		_ = c.Close()
		return existing, nil
	}
	// Evict a dead predecessor (best-effort close) before replacing it.
	if dead, ok := m.clients[key]; ok {
		_ = dead.Close()
	}
	m.clients[key] = c
	return c, nil
}

// CloseAll shuts down every cached LSP client. Call on session teardown
// to avoid leaking gopls / rust-analyzer / pyright processes. Idempotent
// and safe to call concurrently with in-flight ClientFor calls: after it
// returns, the manager is closed and further ClientFor calls error.
func (m *LSPClientManager) CloseAll() {
	m.mu.Lock()
	old := m.clients
	m.clients = map[string]*lsp.Client{}
	m.closed = true
	m.mu.Unlock()

	for _, c := range old {
		_ = c.Close()
	}
}

// activeKeys returns the cache keys with a currently-live client. Used
// by tests and a future TUI diagnostics surface that wants to enumerate
// running servers under the read lock without launching anything.
func (m *LSPClientManager) activeKeys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.clients))
	for k, c := range m.clients {
		if c.Alive() {
			keys = append(keys, k)
		}
	}
	return keys
}

// launch is the indirection point for tests to substitute a fake LSP
// server without spawning a real binary. Defaults to lsp.Launch.
var launch = lsp.Launch

// errManagerClosed is returned by ClientFor after CloseAll.
var errManagerClosed = managerClosedError{}

type managerClosedError struct{}

func (managerClosedError) Error() string { return "lspfind: client manager is closed" }
