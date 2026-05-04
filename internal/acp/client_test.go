package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeClient wires a Client up to an in-memory bidirectional pipe and
// returns the agent-side ends so tests can drive the wrapped-agent
// role: write requests/responses to agentToClient, read the client's
// emissions from clientToAgent.
type pipeClient struct {
	client          *Client
	agentToClient   *io.PipeWriter
	clientToAgent   *bufio.Reader
	closeAgentSides func()
}

func newPipeClient(t *testing.T, onUpdate SessionUpdateHandler) *pipeClient {
	t.Helper()
	clientStdin, agentToClient := io.Pipe()
	clientToAgent, clientStdout := io.Pipe()
	c := NewClient(clientStdin, clientStdout, onUpdate)
	t.Cleanup(func() {
		_ = c.Close(io.EOF)
		_ = agentToClient.Close()
		_ = clientStdout.Close()
	})
	return &pipeClient{
		client:        c,
		agentToClient: agentToClient,
		clientToAgent: bufio.NewReader(clientToAgent),
		closeAgentSides: func() {
			_ = agentToClient.Close()
			_ = clientStdout.Close()
		},
	}
}

// readNext blocks until the client emits one line, returning it
// trimmed of the trailing newline. Times out after 2s with t.Fatal.
func (p *pipeClient) readNext(t *testing.T) []byte {
	t.Helper()
	type r struct {
		line []byte
		err  error
	}
	ch := make(chan r, 1)
	go func() {
		line, err := p.clientToAgent.ReadBytes('\n')
		ch <- r{line, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("read from client: %v", got.err)
		}
		return []byte(strings.TrimRight(string(got.line), "\n"))
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for client emission")
		return nil
	}
}

// sendRequest writes one JSON-RPC request line to the client.
func (p *pipeClient) sendRequest(t *testing.T, id int64, method string, params any) {
	t.Helper()
	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	buf, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := p.agentToClient.Write(append(buf, '\n')); err != nil {
		t.Fatalf("write to client: %v", err)
	}
}

// parseResponse extracts id + result + error from a response line.
func parseResponse(t *testing.T, line []byte) (id int64, result json.RawMessage, rpcErr *RPCError) {
	t.Helper()
	var resp struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("parse response %q: %v", line, err)
	}
	return resp.ID, resp.Result, resp.Error
}

func TestInboundRequest_NoHandler_ReturnsMethodNotFound(t *testing.T) {
	pc := newPipeClient(t, nil)
	pc.sendRequest(t, 1, "fs/read_text_file", map[string]string{"path": "/etc/hosts"})

	id, result, rpcErr := parseResponse(t, pc.readNext(t))
	if id != 1 {
		t.Errorf("response id = %d, want 1", id)
	}
	if rpcErr == nil {
		t.Fatal("expected error, got nil")
	}
	if rpcErr.Code != CodeMethodNotFound {
		t.Errorf("error code = %d, want %d (MethodNotFound)", rpcErr.Code, CodeMethodNotFound)
	}
	if len(result) > 0 {
		t.Errorf("expected no result, got %s", result)
	}
}

func TestInboundRequest_HandlerSuccess(t *testing.T) {
	pc := newPipeClient(t, nil)
	pc.client.SetRequestHandler(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "fs/read_text_file" {
			t.Errorf("handler got method %q", method)
		}
		return map[string]string{"content": "hello\n"}, nil
	})
	pc.sendRequest(t, 7, "fs/read_text_file", map[string]string{"path": "/tmp/x"})

	id, result, rpcErr := parseResponse(t, pc.readNext(t))
	if id != 7 {
		t.Errorf("response id = %d, want 7", id)
	}
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var got map[string]string
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if got["content"] != "hello\n" {
		t.Errorf("result.content = %q, want %q", got["content"], "hello\n")
	}
}

func TestInboundRequest_HandlerNilResultSerialisesAsNull(t *testing.T) {
	pc := newPipeClient(t, nil)
	pc.client.SetRequestHandler(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return nil, nil
	})
	pc.sendRequest(t, 9, "fs/write_text_file", nil)

	line := pc.readNext(t)
	// JSON-RPC 2.0 requires `result` field on success even when null.
	if !strings.Contains(string(line), `"result":null`) {
		t.Errorf("response missing `\"result\":null` field: %s", line)
	}
}

func TestInboundRequest_HandlerRPCError_PreservesCode(t *testing.T) {
	pc := newPipeClient(t, nil)
	pc.client.SetRequestHandler(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "bad path"}
	})
	pc.sendRequest(t, 11, "fs/read_text_file", nil)

	_, _, rpcErr := parseResponse(t, pc.readNext(t))
	if rpcErr == nil {
		t.Fatal("expected error")
	}
	if rpcErr.Code != CodeInvalidParams {
		t.Errorf("code = %d, want %d", rpcErr.Code, CodeInvalidParams)
	}
	if rpcErr.Message != "bad path" {
		t.Errorf("message = %q, want %q", rpcErr.Message, "bad path")
	}
}

func TestInboundRequest_HandlerPlainError_BecomesInternalError(t *testing.T) {
	pc := newPipeClient(t, nil)
	pc.client.SetRequestHandler(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return nil, errors.New("disk on fire")
	})
	pc.sendRequest(t, 13, "fs/read_text_file", nil)

	_, _, rpcErr := parseResponse(t, pc.readNext(t))
	if rpcErr == nil {
		t.Fatal("expected error")
	}
	if rpcErr.Code != CodeInternalError {
		t.Errorf("code = %d, want %d (InternalError)", rpcErr.Code, CodeInternalError)
	}
	if !strings.Contains(rpcErr.Message, "disk on fire") {
		t.Errorf("message = %q, expected to contain underlying error", rpcErr.Message)
	}
}

func TestInboundRequest_SlowHandlerDoesNotBlockReadLoop(t *testing.T) {
	pc := newPipeClient(t, nil)
	var fastDone sync.WaitGroup
	fastDone.Add(1)
	pc.client.SetRequestHandler(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "slow" {
			time.Sleep(150 * time.Millisecond)
			return "slow-done", nil
		}
		fastDone.Done()
		return "fast-done", nil
	})

	// Fire slow first, then fast immediately.
	pc.sendRequest(t, 1, "slow", nil)
	pc.sendRequest(t, 2, "fast", nil)

	// Fast should respond first because slow is parked in its own
	// goroutine; readLoop dispatched fast without waiting on slow.
	first := pc.readNext(t)
	id1, _, _ := parseResponse(t, first)
	if id1 != 2 {
		t.Errorf("first response id = %d, want 2 (fast must arrive before slow)", id1)
	}
	second := pc.readNext(t)
	id2, _, _ := parseResponse(t, second)
	if id2 != 1 {
		t.Errorf("second response id = %d, want 1 (slow)", id2)
	}
}

func TestInboundRequest_ClientClose_DropsPendingResponse(t *testing.T) {
	pc := newPipeClient(t, nil)
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	pc.client.SetRequestHandler(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		close(handlerEntered)
		<-releaseHandler
		return "would-be-result", nil
	})
	pc.sendRequest(t, 1, "anything", nil)

	<-handlerEntered
	// Close the client BEFORE the handler returns.
	_ = pc.client.Close(io.ErrUnexpectedEOF)
	close(releaseHandler)

	// No response should be written. Verify by attempting a non-
	// blocking-ish read with a small budget; we expect EOF or
	// nothing within the window.
	type r struct {
		line []byte
		err  error
	}
	ch := make(chan r, 1)
	go func() {
		line, err := pc.clientToAgent.ReadBytes('\n')
		ch <- r{line, err}
	}()
	select {
	case got := <-ch:
		// Acceptable: pipe-read EOF (closed transport), or the
		// response we explicitly want NOT to see.
		if got.err == nil {
			t.Errorf("expected no response after close, got: %s", got.line)
		}
	case <-time.After(200 * time.Millisecond):
		// Also acceptable: nothing read at all (the writeResponse
		// short-circuited on closed.Load()).
	}
}
