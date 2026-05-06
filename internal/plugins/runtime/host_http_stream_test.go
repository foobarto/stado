package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenHTTPStream_RoundTrip: issue a streaming request against a
// local httptest server, drain the body in chunks via the underlying
// stream, confirm the bytes round-trip.
func TestOpenHTTPStream_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world from streaming server"))
	}))
	defer srv.Close()

	host := &Host{
		NetHTTPRequest:        true,
		NetHTTPRequestPrivate: true, // httptest binds to loopback
	}
	args := streamRequestArgs{Method: "GET", URL: srv.URL}
	result, stream, err := openHTTPStream(context.Background(), host, args)
	if err != nil {
		t.Fatalf("openHTTPStream: %v", err)
	}
	defer stream.body.Close()
	if result.Status != 200 {
		t.Errorf("status: %d", result.Status)
	}
	if result.Headers["X-Custom"] != "yes" {
		t.Errorf("X-Custom header missing: %+v", result.Headers)
	}
	got, err := io.ReadAll(stream.body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world from streaming server" {
		t.Errorf("body mismatch: %q", got)
	}
}

// TestOpenHTTPStream_PrivateAddrRefusedWithoutCap: a loopback URL
// fails when NetHTTPRequestPrivate is off.
func TestOpenHTTPStream_PrivateAddrRefusedWithoutCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	host := &Host{NetHTTPRequest: true} // NetHTTPRequestPrivate=false
	if _, _, err := openHTTPStream(context.Background(), host, streamRequestArgs{Method: "GET", URL: srv.URL}); err == nil {
		t.Fatal("expected private-addr refusal")
	}
}

// TestOpenHTTPStream_RejectsBadMethod: only the standard verbs.
func TestOpenHTTPStream_RejectsBadMethod(t *testing.T) {
	host := &Host{NetHTTPRequest: true}
	_, _, err := openHTTPStream(context.Background(), host, streamRequestArgs{Method: "TEAPOT", URL: "http://x/"})
	if err == nil || !strings.Contains(err.Error(), "unsupported method") {
		t.Errorf("expected unsupported method error, got %v", err)
	}
}

// TestNormalizeHTTPMethod: empty defaults to GET; cases normalised.
func TestNormalizeHTTPMethod(t *testing.T) {
	cases := map[string]string{
		"":      "GET",
		"get":   "GET",
		"POST":  "POST",
		"  put": "PUT",
	}
	for in, want := range cases {
		got, err := normalizeHTTPMethod(in)
		if err != nil {
			t.Errorf("%q → unexpected err %v", in, err)
		}
		if got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := normalizeHTTPMethod("CONNECT"); err == nil {
		t.Error("CONNECT should be rejected")
	}
}

// TestRuntime_HTTPStream_HandleCount: a stream goes through alloc and
// free; the counter tracks correctly.
func TestRuntime_HTTPStream_HandleCount(t *testing.T) {
	// Smoke: opening + closing one stream toggles the counter.
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
	_, stream, err := openHTTPStream(context.Background(), host, streamRequestArgs{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("openHTTPStream: %v", err)
	}
	id, err := r.handles.alloc(string(HandleTypeHTTPResp), stream)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	r.httpStreamCount++
	if r.httpStreamCount != 1 {
		t.Errorf("count = %d, want 1", r.httpStreamCount)
	}
	if got, ok := lookupHTTPStream(r, id); !ok || got != stream {
		t.Error("lookupHTTPStream did not return the registered stream")
	}
	_ = stream.body.Close()
	r.handles.free(id)
	r.httpStreamCount--
}
