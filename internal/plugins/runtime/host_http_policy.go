package runtime

import (
	"net/url"
	"strings"
)

// hostInRequestAllowList returns true when the request URL's hostname
// is reachable under the host's HTTP capabilities. Two cases:
//
//   - host.NetReqHost is empty: the manifest holds the broad
//     net:http_request capability, so any (public) host is in scope —
//     return true.
//   - host.NetReqHost is non-empty: the manifest declares
//     net:http_request:<hostname> for one or more hostnames; the URL's
//     host must match one of them (case-insensitive).
//
// Helper is shared by stado_http_request and stado_http_request_stream
// so both surfaces enforce the same per-host allowlist. Before this
// helper existed, the streaming path skipped the per-host check
// entirely — a plugin granted net:http_request:api.example.com could
// stream from any public host. Reported in the 2026-05-09 review.
//
// A malformed URL (url.Parse error) is treated as denied, on the same
// fail-closed principle as the rest of the policy gates: the caller
// has no business reaching a hostname it can't even spell.
func hostInRequestAllowList(host *Host, rawURL string) bool {
	if len(host.NetReqHost) == 0 {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	hostName := strings.ToLower(u.Hostname())
	for _, a := range host.NetReqHost {
		if strings.EqualFold(strings.TrimSpace(a), hostName) {
			return true
		}
	}
	return false
}
