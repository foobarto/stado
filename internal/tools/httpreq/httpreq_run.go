//go:build !airgap

package httpreq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/foobarto/stado/pkg/tool"
)

const (
	defaultTimeout       = 15 * time.Second
	maxTimeout           = 120 * time.Second
	maxResponseBodyBytes = 4 * 1024 * 1024
)

// httpReqDialContext is overridable for tests (see httpreq_run_test.go).
var httpReqDialContext = guardedHTTPReqDialContext

// blockedHTTPReqPrefixes mirrors webfetch's private-network filter.
// Same blocklist; new packages keep their own copy so a future tweak
// to one path doesn't accidentally affect the other (different threat
// models).
var blockedHTTPReqPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Hop-by-hop headers a plugin should never set; per RFC 7230 these
// are connection-scoped, not end-to-end. Filter them out of args.
var stripRequestHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"transfer-encoding":   {},
	"te":                  {},
	"trailer":             {},
	"upgrade":             {},
	"proxy-authorization": {},
	"proxy-authenticate":  {},
	"host":                {}, // url-derived
	"content-length":      {}, // body-derived
}

func (RequestTool) Run(ctx context.Context, raw json.RawMessage, _ tool.Host) (tool.Result, error) {
	var p Args
	if err := json.Unmarshal(raw, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	method, err := validateMethod(p.Method)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	u, err := validateRequestURL(p.URL)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	initialHost := strings.ToLower(u.Hostname())

	timeout := defaultTimeout
	if p.TimeoutMs > 0 {
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
	}

	var body io.Reader
	if p.BodyB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(p.BodyB64)
		if err != nil {
			err = fmt.Errorf("body_b64 invalid base64: %w", err)
			return tool.Result{Error: err.Error()}, err
		}
		body = bytes.NewReader(decoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	req.Header.Set("User-Agent", "stado/0.1.0")
	for k, v := range p.Headers {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		if _, blocked := stripRequestHeaders[key]; blocked {
			continue
		}
		req.Header.Set(k, v)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = httpReqDialContext
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("http_request: stopped after %d redirects", len(via))
			}
			if err := validateRedirectURL(req.URL, initialHost); err != nil {
				return err
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer resp.Body.Close()

	bodyBytes, truncated, err := readResponseBody(resp.Body)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		// Single-value form is enough for plugin work; multi-value
		// headers (Set-Cookie etc.) get folded comma-joined here.
		// Plugins that care can re-split.
		headers[k] = strings.Join(vs, ", ")
	}

	out := Response{
		Status:        resp.StatusCode,
		Headers:       headers,
		BodyB64:       base64.StdEncoding.EncodeToString(bodyBytes),
		BodyTruncated: truncated,
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: string(payload)}, nil
}

func validateMethod(m string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(m))
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD":
		return method, nil
	default:
		return "", fmt.Errorf("http_request: unsupported method %q", m)
	}
}

func validateRequestURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("http_request: unsupported URL scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("http_request: URL must include a host")
	}
	return u, nil
}

func validateRedirectURL(u *url.URL, initialHost string) error {
	if u == nil {
		return fmt.Errorf("http_request: redirect missing URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("http_request: redirect to unsupported URL scheme %q denied", u.Scheme)
	}
	if strings.ToLower(u.Hostname()) != initialHost {
		return fmt.Errorf("http_request: redirect to different host %q denied", u.Hostname())
	}
	return nil
}

func readResponseBody(r io.Reader) ([]byte, bool, error) {
	var b bytes.Buffer
	if _, err := b.ReadFrom(io.LimitReader(r, maxResponseBodyBytes+1)); err != nil {
		return nil, false, err
	}
	body := b.Bytes()
	if len(body) > maxResponseBodyBytes {
		return body[:maxResponseBodyBytes], true, nil
	}
	return body, false, nil
}

func guardedHTTPReqDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := resolveHTTPReqHost(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !isPublicHTTPReqIP(ip) {
			return nil, fmt.Errorf("http_request: private network address %s for host %q denied", ip, host)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("http_request: no address records for host %q", host)
	}
	dialer := &net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func resolveHTTPReqHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, ip := range addrs {
		out = append(out, ip.Unmap())
	}
	return out, nil
}

func isPublicHTTPReqIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, prefix := range blockedHTTPReqPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
