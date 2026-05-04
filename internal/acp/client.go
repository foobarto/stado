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
	"sync"
	"sync/atomic"
)

// SessionUpdateHandler is invoked on every inbound session/update
// notification. The handler runs on the client's read goroutine — it
// must not block; producers (e.g. an agent.Provider implementation)
// should buffer or post to a channel.
type SessionUpdateHandler func(sessionID string, update json.RawMessage)

// Client is a JSON-RPC 2.0 client speaking the Zed-canonical ACP
// dialect to a wrapped agent's stdio.
type Client struct {
	w  io.Writer
	br *bufio.Reader

	mu       sync.Mutex
	nextID   atomic.Int64
	pending  map[int64]chan rpcReply
	handler  SessionUpdateHandler
	closed   atomic.Bool
	closeErr error
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
		if probe.Method != "" && probe.ID == nil {
			// Notification.
			c.dispatchNotification(probe.Method, probe.Params)
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

func (c *Client) writeMessage(v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
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
	// Empty for the proof-of-concept — stado-as-client doesn't yet
	// advertise any tool-host capabilities back to the wrapped agent.
	// Phase B (per the user's plan) will populate this so wrapped
	// agents can call stado's tool registry via ACP.
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

// SessionNew creates a new session on the wrapped agent.
func (c *Client) SessionNew(ctx context.Context, cwd string) (string, error) {
	raw, err := c.Call(ctx, "session/new", SessionNewParams{CWD: cwd, MCPServers: []any{}})
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
