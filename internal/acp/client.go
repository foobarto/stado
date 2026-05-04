package acp

// JSON-RPC 2.0 client for the Zed-canonical Agent Client Protocol
// (ACP). Pairs with server.go (which speaks the older v0 dialect for
// inbound stado-as-agent connections); the client speaks the
// canonical shape because that's what real-world agents on the
// market emit (gemini --acp, opencode acp, future zed-compatible
// claude wrappers).
//
// Spec reference: https://agentclientprotocol.com/
//
// Method names this client uses:
//
//	initialize        request  → InitializeResult
//	session/new       request  → { sessionId }
//	session/prompt    request  → { stopReason }    (text streamed via session/update)
//	session/update    notif    ← { sessionId, update: {... text/tool deltas ...} }
//	shutdown          request  → {}
//
// The client is stdio-only — it reads/writes line-delimited JSON-RPC
// messages over an io.Reader/io.Writer pair (typically the wrapped
// agent's stdout/stdin). Caller is responsible for spawning the
// subprocess and wiring those pipes in; see internal/providers/acp
// for the full launcher.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// SessionUpdateHandler is invoked on every inbound session/update
// notification. The handler runs on the client's read goroutine — it
// must not block; producers (e.g. an agent.Provider implementation)
// should buffer or post to a channel.
type SessionUpdateHandler func(sessionID string, update json.RawMessage)

// RequestHandler is invoked when the wrapped agent sends an inbound
// JSON-RPC request to stado-as-client. The handler runs on its own
// goroutine — slow handlers do NOT block the client's read loop. The
// returned `result` is JSON-marshalled into the response; returning a
// non-nil error produces a JSON-RPC error response (use *RPCError to
// pick the code, otherwise CodeInternalError is used).
//
// Phase B of EP-0032 wires this to dispatch fs.readTextFile,
// fs.writeTextFile, terminal/* method families to stado's tool
// registry. See `internal/acp/toolhost.go`.
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Client is a JSON-RPC 2.0 client speaking the Zed-canonical ACP
// dialect to a wrapped agent's stdio.
type Client struct {
	w  io.Writer
	br *bufio.Reader

	mu         sync.Mutex
	nextID     atomic.Int64
	pending    map[int64]chan rpcReply
	handler    SessionUpdateHandler
	reqHandler RequestHandler
	closed     atomic.Bool
	closeErr   error
}

type rpcReply struct {
	result json.RawMessage
	err    *RPCError
}

// NewClient wires up the read goroutine immediately. Caller MUST
// arrange for r/w to be closed when done — Client.Close() handles
// the read-side teardown but doesn't own the underlying transport.
func NewClient(r io.Reader, w io.Writer, onUpdate SessionUpdateHandler) *Client {
	c := &Client{
		w:       w,
		br:      bufio.NewReaderSize(r, 1<<16),
		pending: map[int64]chan rpcReply{},
		handler: onUpdate,
	}
	go c.readLoop()
	return c
}

// SetRequestHandler installs a handler for inbound requests from the
// wrapped agent. Must be called before the agent starts sending
// requests (i.e. before Initialize). After the handler is set, any
// inbound request whose method+id are both populated is dispatched
// to it on a fresh goroutine; requests received with no handler set
// receive a CodeMethodNotFound error response.
func (c *Client) SetRequestHandler(h RequestHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqHandler = h
}

// Close stops the read loop and rejects any pending requests with
// the given error (or io.EOF if nil).
func (c *Client) Close(reason error) error {
	if !c.closed.CompareAndSwap(false, true) {
		return c.closeErr
	}
	if reason == nil {
		reason = io.EOF
	}
	c.closeErr = reason
	c.mu.Lock()
	for id, ch := range c.pending {
		ch <- rpcReply{err: &RPCError{Code: CodeInternalError, Message: reason.Error()}}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	return reason
}

func (c *Client) readLoop() {
	defer func() { _ = c.Close(nil) }()
	for {
		line, err := c.br.ReadBytes('\n')
		if err != nil {
			_ = c.Close(err)
			return
		}
		if len(line) == 1 { // empty
			continue
		}
		// Multiplex: a line is either a Response (has id+result/error)
		// or a Notification (has method, no id). Both share the
		// jsonrpc field.
		var probe struct {
			ID     *int64           `json:"id"`
			Method string           `json:"method"`
			Result json.RawMessage  `json:"result"`
			Error  *RPCError        `json:"error"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			// malformed — ignore (some agents print non-JSON greetings)
			continue
		}
		if os.Getenv("STADO_ACP_WIRE_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "[acp wire IN ] %s\n", line)
		}
		if probe.Method != "" && probe.ID == nil {
			// Notification.
			c.dispatchNotification(probe.Method, probe.Params)
			continue
		}
		if probe.Method != "" && probe.ID != nil {
			// Inbound request from the wrapped agent. Dispatch on a
			// fresh goroutine so a slow handler doesn't block the
			// read loop (the agent may pipeline requests).
			c.dispatchRequest(*probe.ID, probe.Method, probe.Params)
			continue
		}
		if probe.ID != nil {
			c.mu.Lock()
			ch, ok := c.pending[*probe.ID]
			delete(c.pending, *probe.ID)
			c.mu.Unlock()
			if ok {
				ch <- rpcReply{result: probe.Result, err: probe.Error}
			}
		}
	}
}

func (c *Client) dispatchRequest(id int64, method string, params json.RawMessage) {
	c.mu.Lock()
	h := c.reqHandler
	c.mu.Unlock()
	if h == nil {
		c.writeResponse(id, nil, &RPCError{
			Code:    CodeMethodNotFound,
			Message: "no inbound request handler registered",
		})
		return
	}
	go func() {
		result, err := h(context.Background(), method, params)
		if err != nil {
			var rpcErr *RPCError
			if errors.As(err, &rpcErr) {
				c.writeResponse(id, nil, rpcErr)
			} else {
				c.writeResponse(id, nil, &RPCError{Code: CodeInternalError, Message: err.Error()})
			}
			return
		}
		c.writeResponse(id, result, nil)
	}()
}

func (c *Client) dispatchNotification(method string, params json.RawMessage) {
	switch method {
	case "session/update":
		if c.handler == nil {
			return
		}
		var p struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		c.handler(p.SessionID, p.Update)
		// Other notifications (server-initiated requests) ignored for now.
	}
}

// Call issues a JSON-RPC request and waits for the matching response
// or context cancellation, whichever comes first. Returns the raw
// result bytes for caller-side unmarshaling.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, c.closeErr
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	if err := c.writeMessage(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case reply := <-ch:
		if reply.err != nil {
			return nil, reply.err
		}
		return reply.result, nil
	}
}

// writeResponse sends a JSON-RPC 2.0 response for an inbound request
// — used by dispatchRequest only. Exactly one of result or rpcErr is
// non-nil. A nil result on success serialises as `"result": null`,
// which is required by the spec when the operation succeeded but
// produced no value.
func (c *Client) writeResponse(id int64, result any, rpcErr *RPCError) {
	if c.closed.Load() {
		return
	}
	resp := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *RPCError       `json:"error,omitempty"`
	}{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		buf, err := json.Marshal(result)
		if err != nil {
			resp.Error = &RPCError{Code: CodeInternalError, Message: "marshal result: " + err.Error()}
		} else {
			resp.Result = buf
		}
	}
	_ = c.writeMessage(resp)
}

func (c *Client) writeMessage(v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if os.Getenv("STADO_ACP_WIRE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[acp wire OUT] %s\n", buf)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(buf); err != nil {
		return err
	}
	if _, err := c.w.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

// --- High-level method wrappers (Zed-canonical shape) ---

// ClientInitializeParams is sent in the `initialize` request.
type ClientInitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *ClientInfo        `json:"clientInfo,omitempty"`
}

type ClientCapabilities struct {
	// FS advertises filesystem capabilities to the wrapped agent.
	// When set, the agent may emit fs/read_text_file and
	// fs/write_text_file requests; stado dispatches them to its tool
	// registry through the configured RequestHandler. Spec:
	// https://agentclientprotocol.com/protocol/file-system
	FS *ClientFSCapabilities `json:"fs,omitempty"`

	// Terminal advertises shell-execution capability. Reserved for a
	// later phase B revision; today stado does NOT advertise terminal
	// (wrapped agent uses its built-in shell or stado's bash via the
	// MCP-mounted registry).
	Terminal bool `json:"terminal,omitempty"`
}

// ClientFSCapabilities is the shape spec-compliant agents check at
// initialize time before emitting fs/* requests. Both flags must
// match a corresponding RequestHandler entry — advertising
// readTextFile=true while having no fs/read_text_file dispatcher
// would cause the agent to call into a void.
type ClientFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// AgentInitializeResult is the canonical-spec shape returned by the
// agent. Different agents include extra fields under
// agentCapabilities (gemini-cli adds promptCapabilities + mcpCapabilities);
// we keep those as raw JSON for caller inspection.
type AgentInitializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentInfo         AgentInfo       `json:"agentInfo"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities"`
	AuthMethods       []AuthMethod    `json:"authMethods,omitempty"`
}

type AgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Initialize calls the `initialize` method.
func (c *Client) Initialize(ctx context.Context, params ClientInitializeParams) (*AgentInitializeResult, error) {
	raw, err := c.Call(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	var out AgentInitializeResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("acp client: parse initialize result: %w", err)
	}
	return &out, nil
}

// SessionNewParams configures a new session. cwd MUST be absolute.
// mcpServers is required (not optional) by some agents — gemini-cli
// uses zod with strict-undefined validation. Default to an empty
// array.
type SessionNewParams struct {
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// SessionNew creates a new session on the wrapped agent. Equivalent
// to SessionNewWithMCPServers(ctx, cwd, nil) — the agent gets an
// empty mcpServers array (required by spec; gemini-cli zod schema
// rejects undefined).
func (c *Client) SessionNew(ctx context.Context, cwd string) (string, error) {
	return c.SessionNewWithMCPServers(ctx, cwd, nil)
}

// SessionNewWithMCPServers creates a new session and mounts the
// supplied MCP server descriptors on it. Each entry in mcpServers
// must marshal to a canonical Zed-spec stdio descriptor
// ({name, command, args, env}) — see EP-0032 phase B (D6) and
// internal/providers/acpwrap/mcpmount.go for the helper that
// produces a stado-as-MCP-server entry.
//
// nil/empty slice produces the same wire as SessionNew (mcpServers
// = []), preserving back-compat for phase A callers.
func (c *Client) SessionNewWithMCPServers(ctx context.Context, cwd string, mcpServers []any) (string, error) {
	if mcpServers == nil {
		mcpServers = []any{}
	}
	raw, err := c.Call(ctx, "session/new", SessionNewParams{CWD: cwd, MCPServers: mcpServers})
	if err != nil {
		return "", err
	}
	var out SessionNewResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("acp client: parse session/new result: %w", err)
	}
	if out.SessionID == "" {
		return "", errors.New("acp client: agent returned empty sessionId")
	}
	return out.SessionID, nil
}

// SessionPromptParams sends a prompt to an existing session.
// `Prompt` is a list of content blocks per the canonical spec; for
// pure text the simplest block is {"type":"text","text":"..."}.
type SessionPromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// ContentBlock is a single multimodal block per the ACP spec.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// SessionPromptResult is what `session/prompt` returns when the turn
// completes. The interesting payload (text, tool calls) arrives via
// session/update notifications during streaming; this result just
// reports why the turn ended.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionPrompt sends a prompt and returns when the agent completes
// the turn (either with a stopReason or a JSON-RPC error). All text
// streams via session/update notifications routed to the
// SessionUpdateHandler passed to NewClient.
func (c *Client) SessionPrompt(ctx context.Context, sessionID, prompt string) (*SessionPromptResult, error) {
	raw, err := c.Call(ctx, "session/prompt", SessionPromptParams{
		SessionID: sessionID,
		Prompt:    []ContentBlock{{Type: "text", Text: prompt}},
	})
	if err != nil {
		return nil, err
	}
	var out SessionPromptResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("acp client: parse session/prompt result: %w", err)
	}
	return &out, nil
}

// Shutdown asks the agent to terminate gracefully.
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.Call(ctx, "shutdown", struct{}{})
	// Errors from shutdown are advisory — the subprocess may already
	// have exited. Caller should still wait on the cmd.
	return err
}
