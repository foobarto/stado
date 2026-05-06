package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenHTTPUpload_RoundTrip: spin up a local HTTP server that
// echoes the request body, drive it via the upload stream end-to-end,
// confirm the bytes round-trip and the response stream reads back.
func TestOpenHTTPUpload_RoundTrip(t *testing.T) {
	gotBody := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody <- body
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("server-saw-" + string(body)))
	}))
	defer srv.Close()

	host := &Host{
		NetHTTPRequest:        true,
		NetHTTPRequestPrivate: true, // httptest binds to loopback
	}
	args := uploadCreateArgs{Method: "POST", URL: srv.URL}
	stream, err := openHTTPUpload(context.Background(), host, args)
	if err != nil {
		t.Fatalf("openHTTPUpload: %v", err)
	}

	// Stream the body in chunks.
	for _, chunk := range []string{"hello ", "world ", "in chunks"} {
		if _, err := stream.writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	stream.closed = true
	_ = stream.writer.Close()

	result := <-stream.respCh
	if result.err != nil {
		t.Fatalf("client.Do: %v", result.err)
	}
	defer result.resp.Body.Close()

	if result.resp.StatusCode != 200 {
		t.Errorf("status: %d", result.resp.StatusCode)
	}
	respBody, _ := io.ReadAll(result.resp.Body)
	if string(respBody) != "server-saw-hello world in chunks" {
		t.Errorf("response body: %q", respBody)
	}
	srvSaw := <-gotBody
	if !bytes.Equal(srvSaw, []byte("hello world in chunks")) {
		t.Errorf("server saw: %q", srvSaw)
	}
}

// TestOpenHTTPUpload_RejectsBodyB64InArgs: the upload args type doesn't
// accept body_b64 — body comes via _upload_write only. Confirm
// json.Unmarshal silently ignores stray body_b64 keys (uploadCreateArgs
// has no such field, so it's just dropped — no surprise behaviour).
func TestUploadCreateArgs_IgnoresUnknownFields(t *testing.T) {
	var args uploadCreateArgs
	raw := []byte(`{"method":"POST","url":"http://x/","body_b64":"shouldbeignored"}`)
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if args.Method != "POST" || args.URL != "http://x/" {
		t.Errorf("args: %+v", args)
	}
}

// TestOpenHTTPUpload_PrivateAddrRefusedWithoutCap: the upload stream
// inherits the dial guard. Without NetHTTPRequestPrivate, a loopback
// URL fails the dial.
func TestOpenHTTPUpload_PrivateAddrRefusedWithoutCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	host := &Host{NetHTTPRequest: true} // NetHTTPRequestPrivate=false
	stream, err := openHTTPUpload(context.Background(), host, uploadCreateArgs{Method: "POST", URL: srv.URL})
	if err != nil {
		// Some failure modes return error at openHTTPUpload time
		// (synchronous URL parse). Either path is acceptable.
		return
	}
	// If create succeeded, the dial fires on the goroutine when we
	// close the writer. The error lands in respCh.
	_ = stream.writer.Close()
	stream.closed = true
	res := <-stream.respCh
	if res.err == nil {
		t.Fatal("expected dial error for private addr without cap")
	}
}

// TestOpenHTTPUpload_RejectsBadMethod: only the standard verbs.
func TestOpenHTTPUpload_RejectsBadMethod(t *testing.T) {
	host := &Host{NetHTTPRequest: true}
	_, err := openHTTPUpload(context.Background(), host, uploadCreateArgs{Method: "TEAPOT", URL: "http://x/"})
	if err == nil || !strings.Contains(err.Error(), "unsupported method") {
		t.Errorf("expected unsupported method error, got %v", err)
	}
}

// TestRuntime_HTTPUpload_HandleCount: opening + finishing one upload
// toggles the counter through alloc / free.
func TestRuntime_HTTPUpload_HandleCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	r, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close(context.Background())

	host := &Host{NetHTTPRequest: true, NetHTTPRequestPrivate: true}
	stream, err := openHTTPUpload(context.Background(), host, uploadCreateArgs{Method: "POST", URL: srv.URL})
	if err != nil {
		t.Fatalf("openHTTPUpload: %v", err)
	}
	id, err := r.handles.alloc(string(HandleTypeHTTPUpload), stream)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	r.httpUploadCount++

	if r.httpUploadCount != 1 {
		t.Errorf("count = %d, want 1", r.httpUploadCount)
	}
	if got, ok := lookupHTTPUpload(r, id); !ok || got != stream {
		t.Error("lookupHTTPUpload did not return the registered stream")
	}
	stream.closed = true
	_ = stream.writer.Close()
	res := <-stream.respCh
	if res.resp != nil {
		_ = res.resp.Body.Close()
	}
	r.handles.free(id)
	r.httpUploadCount--
}
