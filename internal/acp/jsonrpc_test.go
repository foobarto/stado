package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeConn wraps an in-memory pipe pair into a client/server connection.
type pipeConn struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeConn) Close() error                { p.r.Close(); return p.w.Close() }

func newPair() (client, server io.ReadWriteCloser) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return pipeConn{r: cr, w: cw}, pipeConn{r: sr, w: sw}
}

func TestConn_RequestResponse(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewConn(server, server)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(context.Background(), func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method == "ping" {
				return map[string]string{"pong": "yes"}, nil
			}
			return nil, &RPCError{Code: CodeMethodNotFound, Message: "nope"}
		})
	}()

	// Send request.
	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")

	// Read reply.
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"pong":"yes"`) {
		t.Errorf("reply = %q, missing pong", reply)
	}
	if !strings.Contains(reply, `"id":1`) {
		t.Errorf("reply missing id: %q", reply)
	}

	client.Close()
	wg.Wait()
}

func TestConn_MethodNotFound(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewConn(server, server)
	go srv.Serve(context.Background(), func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, &RPCError{Code: CodeMethodNotFound, Message: "missing"}
	})

	io.WriteString(client, `{"jsonrpc":"2.0","id":42,"method":"does-not-exist"}`+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"code":-32601`) {
		t.Errorf("reply should carry -32601 method-not-found: %q", reply)
	}
}

func TestConn_Notification_NoReply(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	called := make(chan string, 1)
	srv := NewConn(server, server)
	go srv.Serve(context.Background(), func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		called <- method
		return nil, nil
	})

	// Notification: no ID.
	io.WriteString(client, `{"jsonrpc":"2.0","method":"notify-me"}`+"\n")

	select {
	case m := <-called:
		if m != "notify-me" {
			t.Errorf("method = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never called")
	}
	// Give the server a moment to *not* write a reply; ensure nothing arrived.
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := client.Read(buf)
		done <- string(buf[:n])
	}()
	select {
	case got := <-done:
		t.Errorf("got unexpected reply for notification: %q", got)
	case <-time.After(150 * time.Millisecond):
		// good — no reply within window
	}
}

func TestConn_ParseError(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewConn(server, server)
	go srv.Serve(context.Background(), func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return "nope", nil
	})

	io.WriteString(client, "not json\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"code":-32700`) {
		t.Errorf("expected parse error code -32700, got %q", reply)
	}
}

func TestConn_Notify_WritesLine(t *testing.T) {
	var buf bytes.Buffer
	c := &Conn{w: &buf, done: make(chan struct{})}
	if err := c.Notify("update", map[string]int{"n": 7}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"method":"update"`) || !strings.Contains(out, `"n":7`) {
		t.Errorf("notify payload missing fields: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("notify should terminate with newline: %q", out)
	}
}

func readLine(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 1024)
		n, _ := r.Read(buf)
		done <- string(buf[:n])
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(timeout):
		t.Fatal("read timeout")
		return ""
	}
}

// TestConn_WaitPendingExceptCaller_OrdersShutdownLast pins the fix for
// dogfood finding #1: a handler that wants its response to land *after*
// all earlier in-flight dispatches must be able to drain them before
// replying. Without WaitPendingExceptCaller, "shutdown" wins the race
// against a slow "work" call and the client sees them out of order.
func TestConn_WaitPendingExceptCaller_OrdersShutdownLast(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewConn(server, server)
	// slow + shutdown handler. slow sleeps 100ms; shutdown waits for
	// pending via the new primitive, then replies. Reading the replies
	// in wire order should give us slow before shutdown.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(context.Background(), func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "slow":
				time.Sleep(100 * time.Millisecond)
				return map[string]string{"finished": "slow"}, nil
			case "shutdown":
				srv.WaitPendingExceptCaller()
				return map[string]string{"finished": "shutdown"}, nil
			}
			return nil, &RPCError{Code: CodeMethodNotFound}
		})
	}()

	// Fire slow then shutdown immediately — shutdown will finish first
	// without the wait primitive.
	io.WriteString(client, `{"jsonrpc":"2.0","id":1,"method":"slow"}`+"\n")
	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"shutdown"}`+"\n")

	first := readLine(t, client, 2*time.Second)
	second := readLine(t, client, 2*time.Second)

	// Wire order: slow's id=1 must come before shutdown's id=2.
	if !strings.Contains(first, `"id":1`) || !strings.Contains(first, "slow") {
		t.Errorf("first reply should be id=1 slow, got %q", first)
	}
	if !strings.Contains(second, `"id":2`) || !strings.Contains(second, "shutdown") {
		t.Errorf("second reply should be id=2 shutdown, got %q", second)
	}

	client.Close()
	wg.Wait()
}

func TestConn_CloseUnblocksServeWithSeparateReader(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer clientWriter.Close()

	srv := NewConn(serverReader, serverWriter)
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return struct{}{}, nil
		})
	}()

	srv.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not exit after Close")
	}
}

func TestConn_RejectsOversizedRequestAndContinues(t *testing.T) {
	client, server := newPair()
	defer client.Close()
	defer server.Close()

	srv := NewConn(server, server)
	go srv.Serve(context.Background(), func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "ping" {
			return map[string]string{"pong": "yes"}, nil
		}
		return nil, &RPCError{Code: CodeMethodNotFound, Message: "nope"}
	})

	oversized := `{"jsonrpc":"2.0","id":1,"method":"` + strings.Repeat("x", maxRequestLineBytes) + `"}`
	io.WriteString(client, oversized+"\n")
	reply := readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"code":-32600`) {
		t.Fatalf("expected invalid-request for oversized frame, got %q", reply)
	}

	io.WriteString(client, `{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	reply = readLine(t, client, 2*time.Second)
	if !strings.Contains(reply, `"pong":"yes"`) {
		t.Fatalf("server did not recover after oversized frame: %q", reply)
	}
}
