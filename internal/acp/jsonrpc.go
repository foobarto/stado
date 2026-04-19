// Package acp implements a subset of Zed's Agent Client Protocol sufficient
// for stado to serve as an editor-side coding agent over stdio.
//
// Wire format: JSON-RPC 2.0, one message per line (LSP-style Content-Length
// framing is NOT used by ACP — it's line-delimited JSON).
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Request is an incoming JSON-RPC 2.0 request. Notifications have no ID.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is an outgoing reply. Exactly one of Result / Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification is a server → client message with no response expected.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Handler runs a single request. If the request is a notification (no ID),
// the returned result is ignored.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Conn is a JSON-RPC 2.0 line-delimited connection. Thread-safe writes.
type Conn struct {
	r    *bufio.Reader
	wMu  sync.Mutex
	w    io.Writer
	done chan struct{}
	once sync.Once
}

// NewConn wraps a reader/writer pair into a JSON-RPC connection.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		r:    bufio.NewReaderSize(r, 64*1024),
		w:    w,
		done: make(chan struct{}),
	}
}

// Close terminates the connection. Safe to call multiple times.
func (c *Conn) Close() {
	c.once.Do(func() { close(c.done) })
}

// Done returns a channel that closes when the peer disconnects.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Serve reads incoming requests until the peer disconnects and dispatches
// them to h. Requests with no ID are treated as notifications — no response
// is sent. Parse errors abort the read loop with an error.
//
// Serve waits for in-flight dispatches to complete before returning, so the
// last response lands on the wire before the connection closes. This matters
// for scripts that pipe a single JSON-RPC request into stado and expect the
// response on stdout.
func (c *Conn) Serve(ctx context.Context, h Handler) error {
	defer c.Close()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		line, err := c.r.ReadBytes('\n')
		if len(line) > 0 {
			wg.Add(1)
			go func(raw []byte) {
				defer wg.Done()
				c.dispatch(ctx, h, raw)
			}(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (c *Conn) dispatch(ctx context.Context, h Handler, raw []byte) {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		c.writeErr(nil, CodeParseError, err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		c.writeErr(req.ID, CodeInvalidRequest, "jsonrpc must be '2.0'")
		return
	}

	result, err := h(ctx, req.Method, req.Params)

	// Notifications (no ID) get no reply, even on error.
	if len(req.ID) == 0 {
		return
	}
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			c.writeErrStruct(req.ID, rpcErr)
		} else {
			c.writeErr(req.ID, CodeInternalError, err.Error())
		}
		return
	}
	c.writeResult(req.ID, result)
}

// Notify sends a server → client notification.
func (c *Conn) Notify(method string, params any) error {
	return c.write(Notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Conn) writeResult(id json.RawMessage, result any) {
	c.write(Response{JSONRPC: "2.0", ID: id, Result: result})
}

func (c *Conn) writeErr(id json.RawMessage, code int, msg string) {
	c.writeErrStruct(id, &RPCError{Code: code, Message: msg})
}

func (c *Conn) writeErrStruct(id json.RawMessage, e *RPCError) {
	c.write(Response{JSONRPC: "2.0", ID: id, Error: e})
}

func (c *Conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wMu.Lock()
	defer c.wMu.Unlock()
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}
