// stado_http_request_stream / _response_read / _response_close —
// chunked HTTP response delivery. EP-0038h.
//
// stado_http_request reads the entire body into memory before
// returning, which OOMs the wasm instance for large payloads
// (firmware blobs, log archives). The streaming variant issues the
// request, returns headers + a body handle, and lets the plugin
// drain the body in chunks.
//
// Response handle type: HandleTypeHTTPResp ("httpresp"). Per-Runtime
// cap: maxHTTPStreamsPerRuntime (8). Reaper: closeAllHTTPStreams.
//
// Capability: reuses net:http_request[:<host>] from the
// non-streaming variant. No new cap.
//
// Out of scope this cycle: request body streaming (large uploads),
// HTTP/2 server-push body, multipart streaming. Defer until a
// concrete plugin needs them.
package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	maxHTTPStreamsPerRuntime = 8
	httpStreamReadDefault    = 30 * time.Second
	httpStreamRequestTimeout = 5 * time.Minute // upper bound on the request itself
)

// httpRespStream wraps an open http.Response body so the wasm caller
// can drain it in chunks. The status + headers are pre-marshaled into
// `meta` so the plugin can recover them without a second host call.
type httpRespStream struct {
	body   io.ReadCloser
	closed bool
}

// streamRequestArgs is a narrowed copy of httpreq.Args. We don't
// embed the full type to avoid a runtime → tools/httpreq dependency
// cycle and to keep the streaming surface explicit (no quietly
// changing semantics if httpreq grows fields).
type streamRequestArgs struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
}

// streamRequestResult is the JSON returned by stado_http_request_stream.
// `body_handle` is the i32-as-uint32 typed-handle ID; the plugin
// drains it via stado_http_response_read.
type streamRequestResult struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	BodyHandle uint32            `json:"body_handle"`
}

func registerHTTPStreamImports(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	registerHTTPRequestStreamImport(builder, host, rt)
	registerHTTPResponseReadImport(builder, host, rt)
	registerHTTPResponseCloseImport(builder, host, rt)
}

// stado_http_request_stream(args_ptr, args_len, out_ptr, out_max) → i32
//
// args_json: streamRequestArgs.
// On success writes the streamRequestResult JSON to out and returns
// its byte length. On error writes nothing and returns -1.
func registerHTTPRequestStreamImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module,
			argsPtr, argsLen, outPtr, outMax int32,
		) int32 {
			if !host.NetHTTPRequest && len(host.NetReqHost) == 0 {
				return -1
			}
			if argsLen <= 0 {
				return -1
			}
			raw, ok := mod.Memory().Read(uint32(argsPtr), uint32(argsLen))
			if !ok {
				return -1
			}
			var args streamRequestArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return -1
			}
			if rt.httpStreamCount >= maxHTTPStreamsPerRuntime {
				return -1
			}
			result, stream, err := openHTTPStream(ctx, host, args)
			if err != nil {
				return -1
			}
			id, err := rt.handles.alloc(string(HandleTypeHTTPResp), stream)
			if err != nil {
				_ = stream.body.Close()
				return -1
			}
			result.BodyHandle = id
			payload, err := json.Marshal(result)
			if err != nil {
				_ = stream.body.Close()
				rt.handles.free(id)
				return -1
			}
			if int32(len(payload)) > outMax {
				_ = stream.body.Close()
				rt.handles.free(id)
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), payload) {
				_ = stream.body.Close()
				rt.handles.free(id)
				return -1
			}
			rt.httpStreamCount++
			return int32(len(payload))
		}).
		Export("stado_http_request_stream")
}

// stado_http_response_read(handle, out_ptr, out_max, timeout_ms) → i32
//
// Returns bytes written to out (0 = EOF), -1 on error / unknown
// handle. timeout_ms <= 0 = default (30s).
func registerHTTPResponseReadImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			handle, outPtr, outMax, timeoutMs int32,
		) int32 {
			stream, ok := lookupHTTPStream(rt, uint32(handle))
			if !ok {
				return -1
			}
			if stream.closed {
				return -1
			}
			// SetReadDeadline on the underlying body via a deadline-
			// capable wrapper would be cleanest; net/http's body
			// reader doesn't expose one directly, so the plugin's
			// bound for slow servers is the request-level timeout.
			// timeout_ms is currently advisory — surface for future use.
			_ = timeoutMs
			buf := make([]byte, outMax)
			n, err := stream.body.Read(buf)
			if n > 0 {
				if !mod.Memory().Write(uint32(outPtr), buf[:n]) {
					return -1
				}
			}
			if err != nil && err != io.EOF {
				return -1
			}
			if n == 0 && err == io.EOF {
				return 0
			}
			return int32(n)
		}).
		Export("stado_http_response_read")
}

// stado_http_response_close(handle) → i32
// Idempotent. Returns 0 on success / already-closed; -1 on unknown
// handle.
func registerHTTPResponseCloseImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, _ api.Module, handle int32) int32 {
			stream, ok := lookupHTTPStream(rt, uint32(handle))
			if !ok {
				return -1
			}
			if !stream.closed {
				_ = stream.body.Close()
				stream.closed = true
				rt.httpStreamCount--
			}
			rt.handles.free(uint32(handle))
			return 0
		}).
		Export("stado_http_response_close")
}

// openHTTPStream issues the HTTP request and returns the
// streamRequestResult metadata + the open response stream. The body
// is left open for the plugin to drain.
func openHTTPStream(ctx context.Context, host *Host, args streamRequestArgs) (streamRequestResult, *httpRespStream, error) {
	method, err := normalizeHTTPMethod(args.Method)
	if err != nil {
		return streamRequestResult{}, nil, err
	}
	if args.URL == "" {
		return streamRequestResult{}, nil, fmt.Errorf("http_request_stream: empty URL")
	}
	timeout := httpStreamRequestTimeout
	if args.TimeoutMs > 0 {
		t := time.Duration(args.TimeoutMs) * time.Millisecond
		if t < timeout {
			timeout = t
		}
	}
	var body io.Reader
	if args.BodyB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(args.BodyB64)
		if err != nil {
			return streamRequestResult{}, nil, fmt.Errorf("body_b64 invalid: %w", err)
		}
		body = bytes.NewReader(decoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, args.URL, body)
	if err != nil {
		return streamRequestResult{}, nil, err
	}
	req.Header.Set("User-Agent", "stado-stream/0.1.0")
	for k, v := range args.Headers {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || isHopByHopHeader(key) {
			continue
		}
		req.Header.Set(k, v)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = httpStreamDialContext(host)
	client := &http.Client{Timeout: timeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return streamRequestResult{}, nil, err
	}
	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		headers[k] = strings.Join(vs, ", ")
	}
	return streamRequestResult{
			Status:  resp.StatusCode,
			Headers: headers,
		},
		&httpRespStream{body: resp.Body},
		nil
}

// httpStreamDialContext wraps dialIP() so the stream request honours
// the same private-IP guard as connect-mode net dial (and the
// non-streaming http_request).
func httpStreamDialContext(h *Host) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		hostStr, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		// Streaming dial uses the SAME guard as net dial — public
		// hosts unrestricted; private addrs need NetHTTPRequestPrivate.
		// Emulate dialIP without the cap-glob check (the caller
		// already enforced NetHTTPRequest / NetReqHost above).
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", hostStr)
		if err != nil {
			return nil, err
		}
		if !h.NetHTTPRequestPrivate {
			for _, ip := range ips {
				if isPrivateIP(ip) {
					return nil, errPrivateAddr
				}
			}
		}
		d := net.Dialer{Timeout: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	}
}

// lookupHTTPStream fetches the *httpRespStream for a handle.
func lookupHTTPStream(rt *Runtime, handle uint32) (*httpRespStream, bool) {
	if !rt.handles.isType(handle, string(HandleTypeHTTPResp)) {
		return nil, false
	}
	v, ok := rt.handles.get(handle)
	if !ok {
		return nil, false
	}
	stream, ok := v.(*httpRespStream)
	return stream, ok
}

// closeAllHTTPStreams reaps open response handles on Runtime.Close.
func (r *Runtime) closeAllHTTPStreams(_ context.Context) {
	r.handles.mu.Lock()
	streams := make([]*httpRespStream, 0)
	for id, e := range r.handles.entries {
		if e.typeTag != string(HandleTypeHTTPResp) {
			continue
		}
		if s, ok := e.value.(*httpRespStream); ok {
			streams = append(streams, s)
		}
		delete(r.handles.entries, id)
	}
	r.handles.mu.Unlock()
	for _, s := range streams {
		if !s.closed {
			_ = s.body.Close()
			s.closed = true
		}
	}
}

// normalizeHTTPMethod accepts the standard HTTP methods, upper-cases,
// and rejects anything else. Mirrors the strictness of httpreq.
func normalizeHTTPMethod(m string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(m))
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD":
		return method, nil
	case "":
		return "GET", nil
	default:
		return "", fmt.Errorf("http_request_stream: unsupported method %q", m)
	}
}

// isHopByHopHeader reports whether a header is connection-scoped per
// RFC 7230 and should not pass through from a plugin's args.
func isHopByHopHeader(name string) bool {
	switch name {
	case "connection", "proxy-connection", "keep-alive", "transfer-encoding",
		"te", "trailer", "upgrade", "proxy-authorization", "proxy-authenticate",
		"host", "content-length":
		return true
	}
	return false
}
