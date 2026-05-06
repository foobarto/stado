//go:build !airgap

package httpreq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"golang.org/x/net/proxy"
)

// configureProxy parses proxyURL and configures `transport` to route
// requests through it. Schemes:
//
//   - http://, https:// — Transport.Proxy returns the parsed URL.
//   - socks5://, socks5h:// — wrap the existing DialContext (preserving
//     the dial guard) with golang.org/x/net/proxy's SOCKS5 dialer.
//
// Other schemes return an error. The host:port portion of `proxyURL`
// is dialed using the supplied `baseDial`, so the existing
// private-network guard applies to the proxy itself — operators must
// grant net:http_request_private when the proxy lives on loopback
// or RFC1918 (the common case for ligolo-ng-style pivots).
func configureProxy(transport *http.Transport, baseDial func(context.Context, string, string) (net.Conn, error), proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("proxy_url parse: %w", err)
	}
	if u.Host == "" {
		return errors.New("proxy_url has no host")
	}
	switch u.Scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
		return nil
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		// Build a parent dialer that wraps baseDial so SOCKS-side
		// connection still goes through the dial guard. The SOCKS
		// dialer gets a contextless parent — wrap to thread ctx.
		parent := contextDialer{base: baseDial}
		dialer, err := proxy.SOCKS5("tcp", u.Host, auth, parent)
		if err != nil {
			return fmt.Errorf("socks5 setup: %w", err)
		}
		ctxDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return errors.New("socks5: returned dialer doesn't support contexts")
		}
		transport.DialContext = ctxDialer.DialContext
		// socks5h means "resolve at the proxy" — the SOCKS5 dialer in
		// x/net/proxy already handles this by NOT pre-resolving when
		// you pass a hostname. The "5h" variant is informational here.
		return nil
	default:
		return fmt.Errorf("proxy_url scheme %q unsupported (want http/https/socks5/socks5h)", u.Scheme)
	}
}

// contextDialer adapts a (ctx, network, addr) → conn function to the
// proxy.Dialer / proxy.ContextDialer interfaces so x/net/proxy's
// SOCKS5 client uses our guarded dial path for the connection TO the
// proxy server itself. The dial to the FINAL destination happens
// proxy-side (for SOCKS5h) or after the SOCKS handshake (SOCKS5).
type contextDialer struct {
	base func(context.Context, string, string) (net.Conn, error)
}

func (d contextDialer) Dial(network, addr string) (net.Conn, error) {
	return d.base(context.Background(), network, addr)
}

func (d contextDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.base(ctx, network, addr)
}
