// Package httpreq is the engine behind the `stado_http_request` host
// import — a generic HTTP client (GET/POST/PUT/DELETE/PATCH/HEAD with
// custom headers and request body) for plugins that need more than the
// markdown-converting GET path that webfetch's `stado_http_get`
// historically provided.
//
// EP-no-internal-tools, Step 1: this used to live under
// `internal/tools/httpreq` and implement `tool.Tool` so the
// `stado_http_request` host import could delegate to a "model-facing"
// tool struct. After Step 1 it's a primitive subsystem package
// (`internal/httpreq`) — no `tool.Tool` interface, no model surface.
// `Do(ctx, args, allowPrivate)` is the public entry point that the
// host wrapper at `internal/plugins/runtime/host_http_request.go`
// calls into.
//
// Wire format (preserved across the refactor):
//
//	request:  { method, url, headers?: {...}, body_b64?, timeout_ms?, proxy_url? }
//	response: { status, headers: {...}, body_b64, body_truncated }
//
// Body is base64 in/out so JSON transport is binary-safe. Same
// private-network dial guard as webfetch: RFC1918, loopback,
// link-local, and the other reserved blocks are refused before TLS
// handshake. `allowPrivate=true` (the host's net:http_request_private
// cap) loosens that to permit lab IPs.
package httpreq

// Run + the dial guard live in httpreq_run.go (`!airgap`) so the
// network path is stripped from airgap builds.

// Args is the JSON-decoded shape of the wasm-side request payload.
type Args struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
	ProxyURL  string            `json:"proxy_url,omitempty"`
}

// Response is the JSON-encoded shape returned to the wasm caller.
type Response struct {
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	BodyB64       string            `json:"body_b64"`
	BodyTruncated bool              `json:"body_truncated"`
}
