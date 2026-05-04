// Package httpreq implements the `stado_http_request` host import — a
// generic HTTP client (POST/PUT/DELETE/PATCH/HEAD/GET with custom
// headers and request body) for plugins that need more than the
// markdown-converting GET path that `stado_http_get` (webfetch)
// provides.
//
// Wire format:
//
//   request:  { method, url, headers?: {...}, body_b64?, timeout_ms? }
//   response: { status, headers: {...}, body_b64, body_truncated }
//
// Body is base64 in/out so JSON transport is binary-safe. Same
// private-network dial guard as webfetch: RFC1918, loopback,
// link-local, and the other reserved blocks are refused before TLS
// handshake. Use a future `net:http_request_private` capability for
// lab IPs.
package httpreq

// Run + the dial guard live in httpreq_run.go (`!airgap`) so the
// network path is stripped from airgap builds.

type RequestTool struct{}

func (RequestTool) Name() string { return "http_request" }
func (RequestTool) Description() string {
	return "Issue an HTTP request with custom method, headers, and body. Returns status, response headers, and body."
}
func (RequestTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"method": map[string]any{
				"type":        "string",
				"description": "HTTP method: GET / POST / PUT / DELETE / PATCH / HEAD",
				"enum":        []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"},
			},
			"url": map[string]any{"type": "string"},
			"headers": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"body_b64": map[string]any{
				"type":        "string",
				"description": "Base64-encoded request body. Empty for GET / HEAD.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Request timeout in milliseconds. Default 15000, max 120000.",
				"minimum":     0,
				"maximum":     120000,
			},
		},
		"required": []string{"method", "url"},
	}
}

type Args struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
}

type Response struct {
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	BodyB64       string            `json:"body_b64"`
	BodyTruncated bool              `json:"body_truncated"`
}
