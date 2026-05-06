// stado_net_* — Tier 1 raw socket primitives.
//
// Tester #5: stado_http_request is HTTP-only. Any non-HTTP protocol —
// SMTP, LDAP, raw TCP banner grab, FTP, custom C2 — requires dropping
// to bash. A stado_tcp_connect(host, port) → handle + stado_tcp_read/
// write(handle, ...) set, gated behind net:tcp_connect:<host>:<port>
// capability, would let WASM plugins talk to arbitrary services.
//
// This cycle ships TCP only. UDP, Unix sockets, listen/accept, ICMP
// are deferred to a future EP-0038g. The handle type is "conn" (per
// EP-0038's typed-prefix convention), already reserved in handles.go.
//
// Capability vocabulary (manifest):
//
//   net:dial:tcp:<host-glob>:<port-glob>
//   net:dial:tcp:*           (broad — any TCP host:port)
//
// Host and port globs use shell-glob semantics. Examples:
//
//   net:dial:tcp:api.example.com:443
//   net:dial:tcp:*.example.com:*
//   net:dial:tcp:127.0.0.1:*    (loopback any port)
//
// The same private-address dial guard as stado_http_request applies:
// dialing RFC1918 / loopback / link-local addresses requires
// net:http_request_private (semantically extended to all dial paths).
// That cap is the operator's "yes, my plugin needs to reach lab IPs"
// signal regardless of which import triggers the dial.
//
// Per-Runtime cap on open conn handles: 64 (matches HTTP client cap).

package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	maxNetConnsPerRuntime = 64
	netDialDefaultTimeout = 10 * time.Second
)

// NetDialAccess records a plugin's manifest-declared outbound socket
// capabilities. EP-0038f shipped TCP; EP-0038g extends with UDP and
// Unix dial. ICMP is still deferred. Each TCP/UDP entry is a
// (host-glob, port-glob) pair; Unix entries are path globs.
type NetDialAccess struct {
	// TCPGlobs are (hostGlob, portGlob) pairs from
	// net:dial:tcp:<hostGlob>:<portGlob> caps. Empty list with the
	// access struct present means no TCP caps granted.
	TCPGlobs []NetDialPattern
	// UDPGlobs mirrors TCPGlobs for net:dial:udp:<host>:<port> caps.
	UDPGlobs []NetDialPattern
	// UnixGlobs are path globs from net:dial:unix:<path-glob>. Path
	// globs use filepath.Match semantics.
	UnixGlobs []string
}

// NetDialPattern is one (host, port) glob pair.
type NetDialPattern struct {
	Host string // shell-glob; "*" = any
	Port string // shell-glob (port as string for glob matching); "*" = any
}

// matchHostPort returns true when (host, port) matches any glob in pats.
// Hosts are matched case-insensitive; "*" host short-circuits to a
// port-only check.
func matchHostPort(pats []NetDialPattern, host, port string) bool {
	host = strings.ToLower(host)
	for _, g := range pats {
		if g.Host == "*" {
			if g.Port == "*" || g.Port == port {
				return true
			}
			continue
		}
		hostMatched, _ := filepath.Match(strings.ToLower(g.Host), host)
		if !hostMatched {
			continue
		}
		if g.Port == "*" {
			return true
		}
		portMatched, _ := filepath.Match(g.Port, port)
		if portMatched {
			return true
		}
	}
	return false
}

// matchPath returns true when path matches any glob in globs (filepath.Match).
func matchPath(globs []string, path string) bool {
	for _, g := range globs {
		if matched, _ := filepath.Match(g, path); matched {
			return true
		}
	}
	return false
}

// CanDialTCP returns true when (host, port) matches any TCPGlobs entry.
func (a *NetDialAccess) CanDialTCP(host, port string) bool {
	if a == nil {
		return false
	}
	return matchHostPort(a.TCPGlobs, host, port)
}

// CanDialUDP returns true when (host, port) matches any UDPGlobs entry.
func (a *NetDialAccess) CanDialUDP(host, port string) bool {
	if a == nil {
		return false
	}
	return matchHostPort(a.UDPGlobs, host, port)
}

// CanDialUnix returns true when path matches any UnixGlobs entry.
func (a *NetDialAccess) CanDialUnix(path string) bool {
	if a == nil {
		return false
	}
	return matchPath(a.UnixGlobs, path)
}

// NetListenAccess records server-side socket capabilities granted by
// net:listen:tcp:<host>:<port> and net:listen:unix:<path> entries.
// Listening on 0.0.0.0 vs 127.0.0.1 is encoded in the capability
// string itself — the operator must spell out which interface to
// expose; there is no implicit fallback.
type NetListenAccess struct {
	TCPGlobs  []NetDialPattern // (hostGlob, portGlob)
	UnixGlobs []string         // path globs
}

// CanListenTCP returns true when (host, port) matches any TCPGlobs entry.
func (a *NetListenAccess) CanListenTCP(host, port string) bool {
	if a == nil {
		return false
	}
	return matchHostPort(a.TCPGlobs, host, port)
}

// CanListenUnix returns true when path matches any UnixGlobs entry.
func (a *NetListenAccess) CanListenUnix(path string) bool {
	if a == nil {
		return false
	}
	return matchPath(a.UnixGlobs, path)
}

// netConn is the host-side state for a stado_net_dial-allocated handle.
type netConn struct {
	c net.Conn
}

// registerNetImports wires the four TCP imports.
func registerNetImports(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	registerNetDialImport(builder, host, rt)
	registerNetReadImport(builder, host, rt)
	registerNetWriteImport(builder, host, rt)
	registerNetCloseImport(builder, host, rt)
}

// stado_net_dial(transport_ptr, transport_len, host_ptr, host_len, port_i32,
//                timeout_ms i32) → i64
//
// transport: "tcp" | "udp" | "unix". For "unix", host carries the
// socket path and port is ignored.
// Returns: handle as i64 (uint32 promoted), or -1 on cap denied / dial
// failure / cap exhausted. The plugin's SDK packages the i64 into the
// typed-prefix "conn:<id>" form for operator-facing display.
func registerNetDialImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module,
			transportPtr, transportLen, hostPtr, hostLen, port, timeoutMs int32,
		) int64 {
			if host.NetDial == nil {
				return -1
			}
			transport, ok := readMemoryString(mod, uint32(transportPtr), uint32(transportLen))
			if !ok {
				return -1
			}
			hostStr, ok := readMemoryString(mod, uint32(hostPtr), uint32(hostLen))
			if !ok {
				return -1
			}
			// Per-Runtime cap (shared TCP+UDP+Unix conn budget).
			if atomic.LoadInt64(&rt.netConnCount) >= maxNetConnsPerRuntime {
				return -1
			}
			timeout := time.Duration(timeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = netDialDefaultTimeout
			}
			conn, err := dialNet(ctx, host, transport, hostStr, int(port), timeout)
			if err != nil {
				return -1
			}
			id, err := rt.handles.alloc(string(HandleTypeConn), &netConn{c: conn})
			if err != nil {
				_ = conn.Close()
				return -1
			}
			atomic.AddInt64(&rt.netConnCount, 1)
			return int64(id)
		}).
		Export("stado_net_dial")
}

// dialNet performs the cap-gated dial. Centralised so each transport
// gets uniform private-IP / cap-glob enforcement.
func dialNet(ctx context.Context, host *Host, transport, hostStr string, port int, timeout time.Duration) (net.Conn, error) {
	switch transport {
	case "tcp":
		portStr := strconv.Itoa(port)
		if !host.NetDial.CanDialTCP(hostStr, portStr) {
			return nil, errCapDenied
		}
		return dialIP(ctx, host, "tcp", hostStr, portStr, timeout)
	case "udp":
		portStr := strconv.Itoa(port)
		if !host.NetDial.CanDialUDP(hostStr, portStr) {
			return nil, errCapDenied
		}
		return dialIP(ctx, host, "udp", hostStr, portStr, timeout)
	default:
		return nil, errCapDenied
	}
}

// dialIP runs the IP-based dial path: pre-resolve, private-IP guard,
// then dial. Used by tcp + udp variants.
func dialIP(ctx context.Context, host *Host, network, hostStr, portStr string, timeout time.Duration) (net.Conn, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", hostStr)
	if err != nil {
		return nil, err
	}
	if !host.NetHTTPRequestPrivate {
		for _, ip := range ips {
			if isPrivateIP(ip) {
				return nil, errPrivateAddr
			}
		}
	}
	d := net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(hostStr, portStr)
	return d.DialContext(ctx, network, addr)
}

// errCapDenied is returned by the dial helpers for cap-glob mismatch.
var errCapDenied = fmt.Errorf("net:dial: capability not granted")

// stado_net_read(handle, out_ptr, out_max, timeout_ms) → i32
// Returns bytes read, 0 on EOF, -1 on error / unknown handle.
func registerNetReadImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module,
			handle, outPtr, outMax, timeoutMs int32,
		) int32 {
			if host.NetDial == nil {
				return -1
			}
			conn, ok := lookupNetConn(rt, uint32(handle))
			if !ok {
				return -1
			}
			if timeoutMs > 0 {
				_ = conn.c.SetReadDeadline(time.Now().Add(time.Duration(timeoutMs) * time.Millisecond))
			} else {
				_ = conn.c.SetReadDeadline(time.Time{})
			}
			buf := make([]byte, outMax)
			n, err := conn.c.Read(buf)
			if err != nil && n == 0 {
				if errors.Is(err, net.ErrClosed) {
					return 0
				}
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), buf[:n]) {
				return -1
			}
			return int32(n)
		}).
		Export("stado_net_read")
}

// stado_net_write(handle, data_ptr, data_len) → i32
// Returns bytes written, -1 on error / unknown handle.
func registerNetWriteImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module,
			handle, dataPtr, dataLen int32,
		) int32 {
			if host.NetDial == nil {
				return -1
			}
			conn, ok := lookupNetConn(rt, uint32(handle))
			if !ok {
				return -1
			}
			data, ok := mod.Memory().Read(uint32(dataPtr), uint32(dataLen))
			if !ok {
				return -1
			}
			n, err := conn.c.Write(data)
			if err != nil {
				return -1
			}
			return int32(n)
		}).
		Export("stado_net_write")
}

// stado_net_close(handle) → i32
// Returns 0 on success or already-closed; -1 on unknown handle.
func registerNetCloseImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, handle int32) int32 {
			if host.NetDial == nil {
				return -1
			}
			conn, ok := lookupNetConn(rt, uint32(handle))
			if !ok {
				return -1
			}
			_ = conn.c.Close()
			rt.handles.free(uint32(handle))
			atomic.AddInt64(&rt.netConnCount, -1)
			return 0
		}).
		Export("stado_net_close")
}

// lookupNetConn fetches the *netConn for a handle, or (nil, false) if
// the handle isn't ours / has been freed.
func lookupNetConn(rt *Runtime, handle uint32) (*netConn, bool) {
	if !rt.handles.isType(handle, string(HandleTypeConn)) {
		return nil, false
	}
	v, ok := rt.handles.get(handle)
	if !ok {
		return nil, false
	}
	conn, ok := v.(*netConn)
	return conn, ok
}

// closeAllNetConns is called on Runtime.Close to reap any TCP
// connections plugins left open.
func (r *Runtime) closeAllNetConns(ctx context.Context) {
	r.handles.mu.Lock()
	conns := make([]*netConn, 0)
	for id, e := range r.handles.entries {
		if e.typeTag != string(HandleTypeConn) {
			continue
		}
		if c, ok := e.value.(*netConn); ok {
			conns = append(conns, c)
		}
		delete(r.handles.entries, id)
	}
	r.handles.mu.Unlock()
	for _, c := range conns {
		_ = c.c.Close()
	}
}

// isPrivateIP reports whether ip is in any of the standard private,
// loopback, or link-local prefixes. Mirrors the dial guard in
// internal/tools/httpreq.
func isPrivateIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip.To16())
	if !ok {
		return false
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified()
}

// errPrivateAddr is returned by the dial guard for clarity in tests.
var errPrivateAddr = fmt.Errorf("net:dial: address is private and net:http_request_private not granted")
