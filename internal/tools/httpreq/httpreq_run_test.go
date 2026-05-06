//go:build !airgap

package httpreq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// privateHost is a fake tool.Host that opts into the loosened guard
// when allow=true. Implements only the subset Run needs (the
// HostNetworkPolicy probe); the embedded tool.Host nil receiver
// would panic if called, but Run never reaches PriorRead/RecordRead
// on the http_request path.
type privateHost struct {
	tool.Host
	allow bool
}

func (p privateHost) AllowPrivateNetwork() bool { return p.allow }

// withTestServer swaps the dial guard so the test server (loopback)
// is reachable. Restored on cleanup. Real-world dial guard remains
// in place outside tests.
func withTestServer(t *testing.T, h http.Handler) (string, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := httpReqDialContext
	httpReqDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	cleanup := func() {
		httpReqDialContext = prev
		srv.Close()
	}
	return srv.URL, cleanup
}

func mustDecode(t *testing.T, raw string) Response {
	t.Helper()
	var r Response
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("decode response: %v (raw=%q)", err, raw)
	}
	return r
}

// TestPostJSON: send a POST with a JSON body and an Authorization
// header; server echoes both. Verifies the request body, headers,
// method are wired correctly, and the response body comes back
// base64-encoded.
func TestPostJSON(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("server got method=%q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer testtoken" {
			t.Errorf("server got Authorization=%q, want 'Bearer testtoken'", got)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write(body)
	}))
	defer stop()

	args := Args{
		Method:  "POST",
		URL:     target + "/api/echo",
		Headers: map[string]string{"Authorization": "Bearer testtoken", "Content-Type": "application/json"},
		BodyB64: base64.StdEncoding.EncodeToString([]byte(`{"x":1}`)),
	}
	raw, _ := json.Marshal(args)
	res, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := mustDecode(t, res.Content)
	if r.Status != 201 {
		t.Fatalf("status=%d, want 201", r.Status)
	}
	body, _ := base64.StdEncoding.DecodeString(r.BodyB64)
	if string(body) != `{"x":1}` {
		t.Fatalf("body=%q, want %q", body, `{"x":1}`)
	}
	if !strings.Contains(strings.ToLower(r.Headers["Content-Type"]), "json") {
		t.Fatalf("response Content-Type=%q, want substring 'json'", r.Headers["Content-Type"])
	}
}

// TestMethods exercises every supported method.
func TestMethods(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(r.Method))
	}))
	defer stop()
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		args := Args{Method: m, URL: target}
		raw, _ := json.Marshal(args)
		res, err := RequestTool{}.Run(context.Background(), raw, nil)
		if err != nil {
			t.Fatalf("Run %s: %v", m, err)
		}
		r := mustDecode(t, res.Content)
		body, _ := base64.StdEncoding.DecodeString(r.BodyB64)
		if string(body) != m {
			t.Fatalf("method %s: body=%q, want %q", m, body, m)
		}
	}
}

// TestUnsupportedMethod
func TestUnsupportedMethod(t *testing.T) {
	args := Args{Method: "TRACE", URL: "https://example.com"}
	raw, _ := json.Marshal(args)
	res, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err == nil {
		t.Fatalf("Run: expected error for TRACE, got result %q", res.Content)
	}
	if !strings.Contains(err.Error(), "unsupported method") {
		t.Fatalf("err=%v, want 'unsupported method'", err)
	}
}

// TestBadBodyB64: malformed base64 surfaces a clean error.
func TestBadBodyB64(t *testing.T) {
	args := Args{Method: "POST", URL: "https://example.com", BodyB64: "%%not-base64%%"}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err == nil || !strings.Contains(err.Error(), "body_b64") {
		t.Fatalf("err=%v, want body_b64 error", err)
	}
}

// TestPrivateNetworkBlocked: the dial guard refuses RFC1918 by
// default. (Test bypass via withTestServer is only for httptest;
// here we exercise the guard directly with a 127.0.0.1 URL.)
func TestPrivateNetworkBlocked(t *testing.T) {
	args := Args{Method: "GET", URL: "http://127.0.0.1:1/blocked"}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("err=%v, want 'private network' error", err)
	}
}

// TestRedirectSameHostAllowed: redirect chain back to the same host
// is followed.
func TestRedirectSameHostAllowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("redirected"))
	})
	target, stop := withTestServer(t, mux)
	defer stop()

	args := Args{Method: "GET", URL: target + "/start"}
	raw, _ := json.Marshal(args)
	res, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := mustDecode(t, res.Content)
	body, _ := base64.StdEncoding.DecodeString(r.BodyB64)
	if string(body) != "redirected" {
		t.Fatalf("body=%q, want 'redirected'", body)
	}
}

// TestRedirectCrossHostBlocked: a redirect to a different host is
// refused (matches webfetch behaviour). Use the test server's URL +
// a real public host that we know exists; we don't actually visit it,
// the redirect rejection happens before any second request.
func TestRedirectCrossHostBlocked(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.com/x", http.StatusFound)
	}))
	defer stop()

	args := Args{Method: "GET", URL: target}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err == nil || !strings.Contains(err.Error(), "different host") {
		t.Fatalf("err=%v, want 'different host'", err)
	}
}

// TestStripsHopByHopHeaders: a plugin trying to set Connection or
// Host gets those silently dropped.
func TestStripsHopByHopHeaders(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Host header always reflects the URL, never the plugin's
		// override; Connection is consumed by the transport.
		// X-Custom should pass through.
		_, _ = w.Write([]byte(r.Header.Get("X-Custom")))
	}))
	defer stop()

	args := Args{
		Method:  "GET",
		URL:     target,
		Headers: map[string]string{"Connection": "Upgrade", "Host": "evil.example", "X-Custom": "kept"},
	}
	raw, _ := json.Marshal(args)
	res, err := RequestTool{}.Run(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := mustDecode(t, res.Content)
	body, _ := base64.StdEncoding.DecodeString(r.BodyB64)
	if string(body) != "kept" {
		t.Fatalf("body=%q, want X-Custom='kept' to pass through", body)
	}
}

// TestResponseBodyTruncated: server returns more than the cap; tool
// truncates and flags it.
func TestResponseBodyTruncated(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write maxResponseBodyBytes + 1024 to trigger truncation.
		buf := make([]byte, maxResponseBodyBytes+1024)
		for i := range buf {
			buf[i] = 'a'
		}
		_, _ = w.Write(buf)
	}))
	defer stop()
	args := Args{Method: "GET", URL: target}
	raw, _ := json.Marshal(args)
	res, _ := RequestTool{}.Run(context.Background(), raw, nil)
	r := mustDecode(t, res.Content)
	if !r.BodyTruncated {
		t.Fatalf("body_truncated=false, want true")
	}
	body, _ := base64.StdEncoding.DecodeString(r.BodyB64)
	if len(body) != maxResponseBodyBytes {
		t.Fatalf("body len=%d, want %d", len(body), maxResponseBodyBytes)
	}
}

// TestPrivateNetworkAllowedWithCapability: a host that implements
// HostNetworkPolicy and returns AllowPrivateNetwork()=true causes
// stado_http_request to use the loosened dial guard, so a loopback
// destination resolves. We exercise this end-to-end against the
// real (un-stubbed) dial path — no test override of
// httpReqDialContext / httpReqPrivateDialContext.
func TestPrivateNetworkAllowedWithCapability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("private-ok"))
	}))
	defer srv.Close()

	args := Args{Method: "GET", URL: srv.URL}
	raw, _ := json.Marshal(args)
	res, err := RequestTool{}.Run(context.Background(), raw, privateHost{allow: true})
	if err != nil {
		t.Fatalf("Run with private cap: %v", err)
	}
	r := mustDecode(t, res.Content)
	if r.Status != 200 {
		t.Fatalf("status=%d, want 200", r.Status)
	}
	body, _ := base64.StdEncoding.DecodeString(r.BodyB64)
	if string(body) != "private-ok" {
		t.Fatalf("body=%q, want 'private-ok'", body)
	}
}

// TestPrivateNetworkDeniedWithoutCapability: same as above but the
// host says allow=false → strict guard kicks in → loopback refused.
// Distinct from TestPrivateNetworkBlocked (which passes nil host).
func TestPrivateNetworkDeniedWithoutCapability(t *testing.T) {
	args := Args{Method: "GET", URL: "http://127.0.0.1:1/blocked"}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, privateHost{allow: false})
	if err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("err=%v, want 'private network' error", err)
	}
}

// TestPrivateGuardStillRefusesMulticast: even with the cap granted,
// multicast / unspecified / docs ranges must remain refused.
func TestPrivateGuardStillRefusesMulticast(t *testing.T) {
	args := Args{Method: "GET", URL: "http://224.0.0.1/x"}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, privateHost{allow: true})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err=%v, want 'denied' error for multicast even with private cap", err)
	}
}

// silence unused-import warning if helper drift removes references.
var _ = url.Parse

// TestProxyURL_HTTP: when proxy_url is set to an http://... URL, the
// configured proxy server receives the request (test using a second
// httptest.Server playing proxy role).
func TestProxyURL_HTTP(t *testing.T) {
	// Backend that the proxy will forward to.
	var backendHits int
	backend, stopBack := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHits++
		w.WriteHeader(200)
		_, _ = w.Write([]byte("via-proxy-ok"))
	}))
	defer stopBack()

	// Proxy that forwards everything to backend regardless of path.
	var proxyHits int
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++
		// Re-issue the request to the backend using a vanilla client.
		req, _ := http.NewRequest(r.Method, backend, r.Body)
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}))
	defer proxySrv.Close()

	args := Args{
		Method:   "GET",
		URL:      "http://example.invalid/x",
		ProxyURL: proxySrv.URL,
	}
	raw, _ := json.Marshal(args)
	res, err := RequestTool{}.Run(context.Background(), raw, privateHost{allow: true})
	if err != nil {
		t.Fatalf("Run with proxy_url: %v", err)
	}
	resp := mustDecode(t, res.Content)
	if resp.Status != 200 {
		t.Errorf("status=%d, want 200", resp.Status)
	}
	if proxyHits == 0 {
		t.Error("proxy was never hit — proxy_url config didn't take effect")
	}
	if backendHits == 0 {
		t.Error("backend was never hit through proxy")
	}
}

// TestProxyURL_RejectsUnsupportedScheme: proxy_url with an
// unsupported scheme is rejected before the request leaves the
// process.
func TestProxyURL_RejectsUnsupportedScheme(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer stop()

	args := Args{
		Method:   "GET",
		URL:      target,
		ProxyURL: "ftp://something.example",
	}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, privateHost{allow: true})
	if err == nil {
		t.Fatal("expected error for unsupported proxy scheme")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error should mention `unsupported`; got: %v", err)
	}
}

// TestProxyURL_RejectsMalformed: garbage proxy_url errors clearly.
func TestProxyURL_RejectsMalformed(t *testing.T) {
	target, stop := withTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer stop()

	args := Args{
		Method:   "GET",
		URL:      target,
		ProxyURL: "://this-is-not-a-url",
	}
	raw, _ := json.Marshal(args)
	_, err := RequestTool{}.Run(context.Background(), raw, privateHost{allow: true})
	if err == nil {
		t.Fatal("expected error for malformed proxy_url")
	}
}
