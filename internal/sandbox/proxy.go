package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Proxy is a minimal HTTPS-tunneling (CONNECT) proxy that enforces a host
// allowlist from a NetPolicy. Run it on a local port and point child
// processes at it via HTTP_PROXY/HTTPS_PROXY — this is the Linux v1 wedge
// per PLAN §3.7 until nftables + net namespaces arrive.
//
// Scope:
//   - HTTPS via CONNECT is fully supported (host allowlist enforced before
//     tunnelling).
//   - Plain HTTP GET/POST is rejected. Clients that need plain HTTP can
//     call the destination directly, but then they're not policed.
//   - AllowAll mode acts as a transparent pass-through.
type Proxy struct {
	Policy   NetPolicy
	Listener net.Listener

	hostMatch func(string) bool
	shutdown  chan struct{}
	wg        sync.WaitGroup
}

// ListenLoopback starts a proxy listening on 127.0.0.1 on a kernel-assigned
// port. Returns the listener's address (host:port) and the Proxy handle.
func ListenLoopback(policy NetPolicy) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sandbox: proxy listen: %w", err)
	}
	p := &Proxy{
		Policy:    policy,
		Listener:  ln,
		hostMatch: compileHostMatch(policy),
		shutdown:  make(chan struct{}),
	}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// Address returns the proxy's listening address (ready to drop into
// HTTP_PROXY / HTTPS_PROXY environment variables as "http://host:port").
func (p *Proxy) Address() string {
	return "http://" + p.Listener.Addr().String()
}

// Close stops accepting connections and waits for in-flight tunnels to end.
// Safe to call multiple times.
func (p *Proxy) Close() error {
	select {
	case <-p.shutdown:
		return nil
	default:
	}
	close(p.shutdown)
	_ = p.Listener.Close()
	p.wg.Wait()
	return nil
}

func (p *Proxy) serve() {
	defer p.wg.Done()
	for {
		conn, err := p.Listener.Accept()
		if err != nil {
			select {
			case <-p.shutdown:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handle(conn)
		}()
	}
}

func (p *Proxy) handle(cli net.Conn) {
	defer cli.Close()
	br := bufio.NewReader(cli)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	if req.Method != http.MethodConnect {
		// Refuse plain HTTP — proxy only mediates HTTPS tunnels.
		writeStatus(cli, http.StatusMethodNotAllowed, "stado proxy: CONNECT only")
		return
	}

	host := hostOnly(req.URL.Host)
	if host == "" {
		host = hostOnly(req.Host)
	}
	if !p.allows(host) {
		writeStatus(cli, http.StatusForbidden, fmt.Sprintf("stado proxy: denied %q by net policy", host))
		return
	}

	upstream, err := net.Dial("tcp", req.URL.Host)
	if err != nil {
		writeStatus(cli, http.StatusBadGateway, "stado proxy: dial: "+err.Error())
		return
	}
	defer upstream.Close()
	writeStatus(cli, http.StatusOK, "Connection established")
	tunnel(cli, upstream)
}

func (p *Proxy) allows(host string) bool {
	if p.hostMatch == nil {
		return false
	}
	return p.hostMatch(host)
}

// compileHostMatch builds a matcher from a NetPolicy. Supported patterns:
//   - exact hostname: "example.com"
//   - wildcard subdomain: "*.example.com" (matches any subdomain, not the
//     apex itself)
//   - CIDR: "10.0.0.0/8", "2001:db8::/32"
func compileHostMatch(p NetPolicy) func(string) bool {
	switch p.Kind {
	case NetAllowAll:
		return func(string) bool { return true }
	case NetDenyAll:
		return func(string) bool { return false }
	}
	// Pre-parse CIDRs; keep wildcards + exact matches as suffix / equality
	// lookups.
	var cidrs []*net.IPNet
	exact := map[string]bool{}
	var suffixes []string
	for _, h := range p.Hosts {
		if _, ipn, err := net.ParseCIDR(h); err == nil {
			cidrs = append(cidrs, ipn)
			continue
		}
		if strings.HasPrefix(h, "*.") {
			suffixes = append(suffixes, strings.ToLower(h[1:])) // keeps leading "."
			continue
		}
		exact[strings.ToLower(h)] = true
	}
	return func(host string) bool {
		h := strings.ToLower(host)
		if exact[h] {
			return true
		}
		for _, s := range suffixes {
			if strings.HasSuffix(h, s) {
				return true
			}
		}
		if ip := net.ParseIP(h); ip != nil {
			for _, n := range cidrs {
				if n.Contains(ip) {
					return true
				}
			}
		}
		return false
	}
}

func tunnel(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

func writeStatus(w net.Conn, code int, reason string) {
	_, _ = w.Write([]byte(fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, reason)))
}

// hostOnly strips an optional :port from host.
func hostOnly(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

// guard against an "unused" warning on url.URL if compileHostMatch changes.
var _ = url.URL{}

// EnvForProxy returns HTTP_PROXY / HTTPS_PROXY env assignments pointing at
// the given proxy — the typical shape for handing to BwrapRunner or any
// subprocess that honours these standard variables.
func EnvForProxy(p *Proxy) []string {
	addr := p.Address()
	return []string{
		"HTTP_PROXY=" + addr,
		"HTTPS_PROXY=" + addr,
		"http_proxy=" + addr,
		"https_proxy=" + addr,
	}
}

// acceptWithTimeout is an internal helper: accept one connection with an
// explicit context. Currently unused externally but kept as a sketch for
// future integration tests that spin proxies up and down.
func acceptWithTimeout(ctx context.Context, ln net.Listener) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
