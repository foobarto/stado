// Package httpclient provides a stateful HTTP client with cookie jar,
// redirect policy, connection limits, and dial guard for allowlist/private-IP enforcement.
package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Sentinel errors returned by Client methods.
var (
	ErrPrivateAddress   = errors.New("httpclient: address is private and AllowPrivate=false")
	ErrHostNotAllowed   = errors.New("httpclient: host is not in AllowedHosts")
	ErrSubdomainEscape  = errors.New("httpclient: redirect target outside original eTLD+1")
	ErrTooManyRedirects = errors.New("httpclient: redirect cap exceeded")
)

// ClientOptions parameterises a stateful HTTP client.
type ClientOptions struct {
	MaxRedirects        int           // default 10
	FollowSubdomainOnly bool          // default false
	MaxConnsPerHost     int           // default 4
	MaxTotalConns       int           // default 32
	Timeout             time.Duration // default 30s; per-request context deadline takes precedence
	AllowedHosts        []string      // exact-match or "*.<domain>" suffix glob; empty = allow all
	AllowPrivate        bool          // when false, RFC1918/loopback/link-local destinations refused
}

// Client is a stateful HTTP client. Safe for concurrent use.
type Client struct {
	inner *http.Client
	jar   http.CookieJar
	opts  ClientOptions
}

// Response is the structured outcome of a request.
type Response struct {
	Status   int
	Headers  map[string][]string
	Body     []byte
	FinalURL string // post-redirect URL
}

// New returns a Client configured with opts. Zero-valued fields are filled with defaults.
func New(opts ClientOptions) (*Client, error) {
	// Apply defaults.
	if opts.MaxRedirects == 0 {
		opts.MaxRedirects = 10
	}
	if opts.MaxConnsPerHost == 0 {
		opts.MaxConnsPerHost = 4
	}
	if opts.MaxTotalConns == 0 {
		opts.MaxTotalConns = 32
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}

	c := &Client{jar: jar, opts: opts}

	dialer := &net.Dialer{Timeout: opts.Timeout}
	transport := &http.Transport{
		MaxConnsPerHost: opts.MaxConnsPerHost,
		MaxIdleConns:    opts.MaxTotalConns,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if err := c.guardDial(host); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
	}

	// CheckRedirect runs before the redirected request is dialled, so the dial guard
	// will enforce AllowedHosts and AllowPrivate on the redirect target too.
	// This function handles the redirect-count cap and FollowSubdomainOnly policy.
	inner := &http.Client{
		Jar:       jar,
		Transport: transport,
		Timeout:   opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= opts.MaxRedirects {
				return ErrTooManyRedirects
			}
			if opts.FollowSubdomainOnly && len(via) > 0 {
				origHost := via[0].URL.Hostname()
				destHost := req.URL.Hostname()
				origETLD1, err1 := publicsuffix.EffectiveTLDPlusOne(origHost)
				destETLD1, err2 := publicsuffix.EffectiveTLDPlusOne(destHost)
				if err1 != nil || err2 != nil || origETLD1 != destETLD1 {
					return ErrSubdomainEscape
				}
			}
			return nil
		},
	}

	c.inner = inner
	return c, nil
}

// guardDial checks AllowedHosts and AllowPrivate for host (which may be a hostname or literal IP).
// It is called from the DialContext hook, after the host has been extracted from the addr.
func (c *Client) guardDial(host string) error {
	// Resolve to IP(s) if not already a literal IP.
	ip := net.ParseIP(host)
	if ip == nil {
		addrs, err := net.LookupHost(host)
		if err != nil || len(addrs) == 0 {
			return err
		}
		ip = net.ParseIP(addrs[0])
	}

	if ip != nil && !c.opts.AllowPrivate {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return ErrPrivateAddress
		}
	}

	if len(c.opts.AllowedHosts) > 0 && !matchesAllowedHosts(host, c.opts.AllowedHosts) {
		return ErrHostNotAllowed
	}

	return nil
}

// matchesAllowedHosts returns true if host matches any pattern in allowed.
// Patterns may be exact hostnames or "*.<domain>" suffix wildcards.
func matchesAllowedHosts(host string, allowed []string) bool {
	for _, pattern := range allowed {
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:] // e.g. ".example.com"
			if strings.HasSuffix(host, suffix) {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}

// extractETLD1 returns the eTLD+1 for a host, or an error.
// Exported only for use in tests that need to exercise the eTLD check directly.
func extractETLD1(host string) (string, error) {
	return publicsuffix.EffectiveTLDPlusOne(host)
}

// Request executes one HTTP method/url through the client.
// Headers are added to the outgoing request as-is. Body may be nil.
func (c *Client) Request(ctx context.Context, method, urlStr string, headers map[string]string, body []byte) (*Response, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.inner.Do(req)
	if err != nil {
		// Unwrap CheckRedirect errors so callers can compare against sentinels.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			if errors.Is(urlErr.Err, ErrTooManyRedirects) {
				return nil, ErrTooManyRedirects
			}
			if errors.Is(urlErr.Err, ErrSubdomainEscape) {
				return nil, ErrSubdomainEscape
			}
		}
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Clone headers so caller mutations don't race with the transport.
	hdrs := make(map[string][]string, len(resp.Header))
	for k, vs := range resp.Header {
		clone := make([]string, len(vs))
		copy(clone, vs)
		hdrs[k] = clone
	}

	return &Response{
		Status:   resp.StatusCode,
		Headers:  hdrs,
		Body:     respBody,
		FinalURL: resp.Request.URL.String(),
	}, nil
}

// Close releases idle connections held by the underlying transport.
func (c *Client) Close() {
	if t, ok := c.inner.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}
