package runtime

// host_http_client.go — wasm host imports for stado_http_client_create,
// stado_http_client_close, and stado_http_client_request (EP-0038e Tier 2).
//
// Design notes:
//
//   - Handle allocation reuses the runtime-shared handleRegistry with typeTag
//     "http". The uint32 handle is returned directly as i64 to the wasm side;
//     the wasm SDK may format it as "http:<hex>" for operator display.
//
//   - AllowedHosts intersection: opts.AllowedHosts (from the plugin) is
//     intersected with host.NetReqHost (operator allowlist). Even if the plugin
//     passes an empty AllowedHosts (allow-all), the effective list is bounded by
//     what the operator granted. When host.NetReqHost is empty the operator
//     placed no extra restriction.
//
//   - AllowPrivate gate: opts.AllowPrivate=true requires host.NetHTTPRequestPrivate.
//     If the cap is absent the create call returns -1.
//
//   - Response shape: _request writes a single JSON envelope into the caller's
//     output buffer:
//       {"status":200,"headers":{...},"final_url":"https://...","body_b64":"<base64>"}
//     Returns the total bytes written, or -1 on error / cap denied.
//
//   - Per-Runtime cap: at most maxHTTPClientsPerRuntime clients may be open at
//     once. Beyond that _create returns -1. Defensive against resource exhaustion.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/httpclient"
)

const maxHTTPClientsPerRuntime = 64

// httpClientCount is a per-Runtime counter of open HTTP client handles.
// It is stored on the Runtime struct via a dedicated field; the atomic
// in this file is a package-level placeholder — see registerHTTPClientImports
// for where the per-runtime counter is threaded through the closures.

func registerHTTPClientImports(builder wazero.HostModuleBuilder, host *Host, r *Runtime) {
	registerHTTPClientCreate(builder, host, r)
	registerHTTPClientClose(builder, host, r)
	registerHTTPClientRequest(builder, host, r)
}

// clientOptsJSON mirrors the JSON shape plugins pass to stado_http_client_create.
type clientOptsJSON struct {
	MaxRedirects        int      `json:"max_redirects"`
	FollowSubdomainOnly bool     `json:"follow_subdomain_only"`
	MaxConnsPerHost     int      `json:"max_conns_per_host"`
	MaxTotalConns       int      `json:"max_total_conns"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	AllowedHosts        []string `json:"allowed_hosts"`
	AllowPrivate        bool     `json:"allow_private"`
}

// intersectHosts returns the intersection of plugin-requested hosts and the
// operator's allowlist. When operatorList is empty, the operator placed no
// restriction and pluginList is returned as-is. When pluginList is empty
// (plugin requested allow-all), the effective list is operatorList.
// When both are non-empty, only hosts present in both are allowed.
func intersectHosts(pluginList, operatorList []string) []string {
	if len(operatorList) == 0 {
		return pluginList // no operator restriction
	}
	if len(pluginList) == 0 {
		return operatorList // plugin allow-all, bounded by operator
	}
	opSet := make(map[string]bool, len(operatorList))
	for _, h := range operatorList {
		opSet[h] = true
	}
	var out []string
	for _, h := range pluginList {
		if opSet[h] {
			out = append(out, h)
		}
	}
	return out
}

// stado_http_client_create(opts_ptr i32, opts_len i32) → i64
//
// Reads JSON-encoded ClientOptions from wasm memory, constructs a
// httpclient.Client, and allocates a handle. Returns the uint32 handle
// promoted to i64 on success, or -1 on cap denial / error.
func registerHTTPClientCreate(builder wazero.HostModuleBuilder, host *Host, r *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			optsPtr := api.DecodeU32(stack[0])
			optsLen := api.DecodeU32(stack[1])

			if !host.NetHTTPClient {
				host.Logger.Warn("stado: warn: net:http_client not declared by plugin", slog.String("plugin", host.Manifest.Name))
				stack[0] = api.EncodeI64(-1)
				return
			}

			// Per-Runtime cap check.
			if atomic.LoadInt64(&r.httpClientCount) >= maxHTTPClientsPerRuntime {
				stack[0] = api.EncodeI64(-1)
				return
			}

			optsBytes, err := readBytesLimited(mod, optsPtr, optsLen, 4096)
			if err != nil {
				stack[0] = api.EncodeI64(-1)
				return
			}

			var jopts clientOptsJSON
			if len(optsBytes) > 0 {
				if err := json.Unmarshal(optsBytes, &jopts); err != nil {
					stack[0] = api.EncodeI64(-1)
					return
				}
			}

			// AllowPrivate gate.
			if jopts.AllowPrivate && !host.NetHTTPRequestPrivate {
				stack[0] = api.EncodeI64(-1)
				return
			}

			effective := httpclient.ClientOptions{
				MaxRedirects:        jopts.MaxRedirects,
				FollowSubdomainOnly: jopts.FollowSubdomainOnly,
				MaxConnsPerHost:     jopts.MaxConnsPerHost,
				MaxTotalConns:       jopts.MaxTotalConns,
				AllowedHosts:        intersectHosts(jopts.AllowedHosts, host.NetReqHost),
				AllowPrivate:        jopts.AllowPrivate,
			}
			if jopts.TimeoutSeconds > 0 {
				effective.Timeout = time.Duration(jopts.TimeoutSeconds) * time.Second
			}

			client, err := httpclient.New(effective)
			if err != nil {
				stack[0] = api.EncodeI64(-1)
				return
			}

			handle, err := r.handles.alloc("http", client)
			if err != nil {
				client.Close()
				stack[0] = api.EncodeI64(-1)
				return
			}

			atomic.AddInt64(&r.httpClientCount, 1)
			stack[0] = api.EncodeI64(int64(handle))
		}),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI64}).
		Export("stado_http_client_create")
}

// stado_http_client_close(handle i32) → i32
//
// Releases the client and frees the handle. Idempotent — closing a
// missing handle returns 0 (not an error).
func registerHTTPClientClose(builder wazero.HostModuleBuilder, host *Host, r *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			if !host.NetHTTPClient {
				host.Logger.Warn("stado: warn: net:http_client not declared by plugin", slog.String("plugin", host.Manifest.Name))
				stack[0] = api.EncodeI32(-1)
				return
			}

			handle := api.DecodeU32(stack[0])
			val, ok := r.handles.get(handle)
			if !ok {
				// Idempotent — missing handle is not an error.
				stack[0] = api.EncodeI32(0)
				return
			}
			if !r.handles.isType(handle, "http") {
				stack[0] = api.EncodeI32(-1)
				return
			}
			if client, ok := val.(*httpclient.Client); ok {
				client.Close()
			}
			r.handles.free(handle)
			atomic.AddInt64(&r.httpClientCount, -1)
			stack[0] = api.EncodeI32(0)
		}),
		[]api.ValueType{api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_http_client_close")
}

// httpResponseJSON is the JSON envelope written to resp_out by _request.
type httpResponseJSON struct {
	Status   int                 `json:"status"`
	Headers  map[string][]string `json:"headers"`
	FinalURL string              `json:"final_url"`
	BodyB64  string              `json:"body_b64"`
}

// stado_http_client_request(handle i32,
//
//	method_ptr i32, method_len i32,
//	url_ptr    i32, url_len    i32,
//	headers_ptr i32, headers_len i32,
//	body_ptr   i32, body_len   i32,
//	resp_out_ptr i32, resp_max i32) → i32
//
// Issues an HTTP request through the handle's client. Response is written
// as a JSON envelope (see httpResponseJSON above) into resp_out. The body
// bytes are base64-encoded inside the envelope.
//
// Returns the total bytes written on success, or -1 on error / cap denied.
func registerHTTPClientRequest(builder wazero.HostModuleBuilder, host *Host, r *Runtime) {
	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			handle := api.DecodeU32(stack[0])
			methodPtr := api.DecodeU32(stack[1])
			methodLen := api.DecodeU32(stack[2])
			urlPtr := api.DecodeU32(stack[3])
			urlLen := api.DecodeU32(stack[4])
			headersPtr := api.DecodeU32(stack[5])
			headersLen := api.DecodeU32(stack[6])
			bodyPtr := api.DecodeU32(stack[7])
			bodyLen := api.DecodeU32(stack[8])
			respOutPtr := api.DecodeU32(stack[9])
			respMax := api.DecodeU32(stack[10])

			if !host.NetHTTPClient {
				host.Logger.Warn("stado: warn: net:http_client not declared by plugin", slog.String("plugin", host.Manifest.Name))
				stack[0] = api.EncodeI32(-1)
				return
			}

			val, ok := r.handles.get(handle)
			if !ok || !r.handles.isType(handle, "http") {
				stack[0] = api.EncodeI32(-1)
				return
			}
			client, ok := val.(*httpclient.Client)
			if !ok {
				stack[0] = api.EncodeI32(-1)
				return
			}

			method, err := readStringLimited(mod, methodPtr, methodLen, 32)
			if err != nil || method == "" {
				stack[0] = api.EncodeI32(-1)
				return
			}
			urlStr, err := readStringLimited(mod, urlPtr, urlLen, 8192)
			if err != nil || urlStr == "" {
				stack[0] = api.EncodeI32(-1)
				return
			}

			var hdrs map[string]string
			if headersLen > 0 {
				hb, err := readBytesLimited(mod, headersPtr, headersLen, 64*1024)
				if err != nil {
					stack[0] = api.EncodeI32(-1)
					return
				}
				if err := json.Unmarshal(hb, &hdrs); err != nil {
					stack[0] = api.EncodeI32(-1)
					return
				}
			}

			var body []byte
			if bodyLen > 0 {
				body, err = readBytesLimited(mod, bodyPtr, bodyLen, 32*1024*1024)
				if err != nil {
					stack[0] = api.EncodeI32(-1)
					return
				}
			}

			resp, err := client.Request(ctx, method, urlStr, hdrs, body)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}

			env := httpResponseJSON{
				Status:   resp.Status,
				Headers:  resp.Headers,
				FinalURL: resp.FinalURL,
				BodyB64:  base64.StdEncoding.EncodeToString(resp.Body),
			}
			payload, err := json.Marshal(env)
			if err != nil {
				stack[0] = api.EncodeI32(-1)
				return
			}

			n := writeBytes(mod, respOutPtr, respMax, payload)
			stack[0] = api.EncodeI32(n)
		}),
		[]api.ValueType{
			api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
			api.ValueTypeI32, api.ValueTypeI32,
		},
		[]api.ValueType{api.ValueTypeI32}).
		Export("stado_http_client_request")
}

// closeAllHTTPClients walks the handle registry and closes every HTTP client.
// Called from Runtime.Close to release idle connections.
func closeAllHTTPClients(r *Runtime) {
	r.handles.mu.Lock()
	var toClose []*httpclient.Client
	var toFree []uint32
	for id, entry := range r.handles.entries {
		if entry.typeTag == "http" {
			if c, ok := entry.value.(*httpclient.Client); ok {
				toClose = append(toClose, c)
				toFree = append(toFree, id)
			}
		}
	}
	r.handles.mu.Unlock()
	for _, c := range toClose {
		c.Close()
	}
	for _, id := range toFree {
		r.handles.free(id)
	}
}
