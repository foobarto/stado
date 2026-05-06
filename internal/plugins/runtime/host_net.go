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
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	maxNetConnsPerRuntime     = 64
	maxNetListenersPerRuntime = 8
	netDialDefaultTimeout     = 10 * time.Second
	// netAcceptMaxTimeout caps stado_net_accept's blocking window. An
	// infinite-block accept holds the runtime forever (DoS); the wasm
	// caller re-loops if it wants to wait longer. EP-0038g Q5.
	netAcceptMaxTimeout     = 30 * time.Second
	netAcceptDefaultTimeout = 5 * time.Second
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
// net:listen:{tcp,udp}:<host>:<port> and net:listen:unix:<path>
// entries. Listening on 0.0.0.0 vs 127.0.0.1 is encoded in the
// capability string itself — the operator must spell out which
// interface to expose; there is no implicit fallback.
type NetListenAccess struct {
	TCPGlobs  []NetDialPattern // (hostGlob, portGlob)
	UDPGlobs  []NetDialPattern // EP-0038h — UDP bind for stateless send/recv
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

// netListener is the host-side state for a stado_net_listen-allocated
// handle. `kind` carries the transport so close-listener knows what to
// reap (socket file for unix; PacketConn vs Listener for udp vs tcp).
type netListener struct {
	l    net.Listener   // tcp / unix; nil for udp
	pc   net.PacketConn // udp; nil for tcp / unix
	kind string         // "tcp" | "unix" | "udp"
	path string         // unix only — removed on Close
}

// closeUnderlying closes whichever of l or pc is set.
func (n *netListener) closeUnderlying() error {
	if n.l != nil {
		return n.l.Close()
	}
	if n.pc != nil {
		return n.pc.Close()
	}
	return nil
}

// registerNetImports wires every EP-0038f/g/h host import: dial+read+
// write+close, plus the listen+accept+close_listener trio, plus the
// stateless UDP sendto+recvfrom pair.
func registerNetImports(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	registerNetDialImport(builder, host, rt)
	registerNetReadImport(builder, host, rt)
	registerNetWriteImport(builder, host, rt)
	registerNetCloseImport(builder, host, rt)
	registerNetListenImport(builder, host, rt)
	registerNetAcceptImport(builder, host, rt)
	registerNetCloseListenerImport(builder, host, rt)
	registerNetSendtoImport(builder, host, rt)
	registerNetRecvfromImport(builder, host, rt)
	registerNetSetoptImport(builder, host, rt)
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
	case "unix":
		// hostStr carries the socket path; port is ignored.
		if err := validateUnixSocketPath(hostStr); err != nil {
			return nil, err
		}
		if !host.NetDial.CanDialUnix(hostStr) {
			return nil, errCapDenied
		}
		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "unix", hostStr)
	default:
		return nil, errCapDenied
	}
}

// validateUnixSocketPath enforces the conservative path constraints
// from EP-0038g Q10: no `..` traversal, BSD sun_path upper bound.
func validateUnixSocketPath(path string) error {
	if path == "" {
		return fmt.Errorf("net:unix: empty path")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("net:unix: path contains `..`")
	}
	if len(path) > maxUnixSocketPath {
		return fmt.Errorf("net:unix: path exceeds %d bytes", maxUnixSocketPath)
	}
	return nil
}

// maxUnixSocketPath is the conservative cross-platform upper bound for
// sockaddr_un.sun_path (BSD = 104, Linux = 108). Pick the smaller.
const maxUnixSocketPath = 104

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

// stado_net_listen(transport_ptr, transport_len, host_ptr, host_len,
//                  port_i32) → i64
//
// transport: "tcp" | "unix". For "unix", host carries the socket path
// and port is ignored. Returns the listener handle as i64, or -1 on
// cap denied / bind failure / cap exhausted.
func registerNetListenImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module,
			transportPtr, transportLen, hostPtr, hostLen, port int32,
		) int64 {
			if host.NetListen == nil {
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
			if atomic.LoadInt64(&rt.netListenerCount) >= maxNetListenersPerRuntime {
				return -1
			}
			lst, err := listenNet(host, transport, hostStr, int(port))
			if err != nil {
				return -1
			}
			id, err := rt.handles.alloc(string(HandleTypeListen), lst)
			if err != nil {
				_ = lst.closeUnderlying()
				if lst.path != "" {
					_ = removeUnixSocketFile(lst.path)
				}
				return -1
			}
			atomic.AddInt64(&rt.netListenerCount, 1)
			return int64(id)
		}).
		Export("stado_net_listen")
}

// listenNet performs the cap-gated bind. Returns a fully-formed
// netListener; tcp/unix populate `l` and udp populates `pc`.
func listenNet(host *Host, transport, hostStr string, port int) (*netListener, error) {
	switch transport {
	case "tcp":
		portStr := strconv.Itoa(port)
		if !host.NetListen.CanListenTCP(hostStr, portStr) {
			return nil, errCapDenied
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(hostStr, portStr))
		if err != nil {
			return nil, err
		}
		return &netListener{l: ln, kind: "tcp"}, nil
	case "unix":
		if err := validateUnixSocketPath(hostStr); err != nil {
			return nil, err
		}
		if !host.NetListen.CanListenUnix(hostStr) {
			return nil, errCapDenied
		}
		ln, err := net.Listen("unix", hostStr)
		if err != nil {
			return nil, err
		}
		return &netListener{l: ln, kind: "unix", path: hostStr}, nil
	case "udp":
		// UDP listen is shape-shared with TCP listen but uses
		// PacketConn semantics (sendto / recvfrom). Cap vocab reuses
		// net:listen:tcp's host-port glob shape under :udp:.
		portStr := strconv.Itoa(port)
		if host.NetListen == nil {
			return nil, errCapDenied
		}
		if !matchHostPort(host.NetListen.UDPGlobs, hostStr, portStr) {
			return nil, errCapDenied
		}
		pc, err := net.ListenPacket("udp", net.JoinHostPort(hostStr, portStr))
		if err != nil {
			return nil, err
		}
		return &netListener{pc: pc, kind: "udp"}, nil
	default:
		return nil, errCapDenied
	}
}

// stado_net_accept(lst_handle i32, timeout_ms i32) → i64
//
// Returns the accepted conn handle (uint32 promoted to i64) on success.
// Returns -1 on error / unknown handle / cap exhausted, -2 on timeout.
// Timeout is clamped to [0, netAcceptMaxTimeout]; <=0 = default 5s.
func registerNetAcceptImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, handle, timeoutMs int32) int64 {
			if host.NetListen == nil {
				return -1
			}
			lst, ok := lookupNetListener(rt, uint32(handle))
			if !ok {
				return -1
			}
			if lst.l == nil {
				// UDP / packet-conn listeners have no Accept; plugin
				// should be using sendto/recvfrom instead.
				return -1
			}
			timeout := time.Duration(timeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = netAcceptDefaultTimeout
			}
			if timeout > netAcceptMaxTimeout {
				timeout = netAcceptMaxTimeout
			}
			if d, ok := lst.l.(interface{ SetDeadline(time.Time) error }); ok {
				_ = d.SetDeadline(time.Now().Add(timeout))
				defer d.SetDeadline(time.Time{})
			}
			if atomic.LoadInt64(&rt.netConnCount) >= maxNetConnsPerRuntime {
				return -1
			}
			conn, err := lst.l.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					return -2
				}
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
		Export("stado_net_accept")
}

// stado_net_close_listener(lst_handle) → i32
// Returns 0 on success / already-closed; -1 on unknown handle. Removes
// the unix socket file if the listener was a unix bind. Idempotent.
func registerNetCloseListenerImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, handle int32) int32 {
			if host.NetListen == nil {
				return -1
			}
			lst, ok := lookupNetListener(rt, uint32(handle))
			if !ok {
				return -1
			}
			_ = lst.closeUnderlying()
			if lst.kind == "unix" && lst.path != "" {
				_ = removeUnixSocketFile(lst.path)
			}
			rt.handles.free(uint32(handle))
			atomic.AddInt64(&rt.netListenerCount, -1)
			return 0
		}).
		Export("stado_net_close_listener")
}

// lookupNetListener fetches the *netListener for a handle, or
// (nil, false) if the handle isn't ours.
func lookupNetListener(rt *Runtime, handle uint32) (*netListener, bool) {
	if !rt.handles.isType(handle, string(HandleTypeListen)) {
		return nil, false
	}
	v, ok := rt.handles.get(handle)
	if !ok {
		return nil, false
	}
	lst, ok := v.(*netListener)
	return lst, ok
}

// stado_net_sendto(lst_handle, host_ptr, host_len, port_i32,
//                  data_ptr, data_len) → i32
//
// Sends one UDP packet to the (host, port) peer. Cap-gated by the
// SAME net:dial:udp:<peer-host>:<peer-port> globs as connect-mode UDP
// dial — a UDP listener can't be a wildcard spray gun. Private peer
// addresses still need NetHTTPRequestPrivate. Returns bytes written
// or -1 on cap denied / unknown handle / send failure.
func registerNetSendtoImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			handle, hostPtr, hostLen, port, dataPtr, dataLen int32,
		) int32 {
			lst, ok := lookupNetListener(rt, uint32(handle))
			if !ok || lst.kind != "udp" || lst.pc == nil {
				return -1
			}
			if host.NetDial == nil {
				return -1
			}
			peerHost, ok := readMemoryString(mod, uint32(hostPtr), uint32(hostLen))
			if !ok {
				return -1
			}
			portStr := strconv.Itoa(int(port))
			if !host.NetDial.CanDialUDP(peerHost, portStr) {
				return -1
			}
			// Private-IP guard — same as dial.
			if !host.NetHTTPRequestPrivate {
				if ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", peerHost); err == nil {
					for _, ip := range ips {
						if isPrivateIP(ip) {
							return -1
						}
					}
				}
			}
			data, ok := mod.Memory().Read(uint32(dataPtr), uint32(dataLen))
			if !ok {
				return -1
			}
			addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(peerHost, portStr))
			if err != nil {
				return -1
			}
			n, err := lst.pc.WriteTo(data, addr)
			if err != nil {
				return -1
			}
			return int32(n)
		}).
		Export("stado_net_sendto")
}

// stado_net_recvfrom(lst_handle, timeout_ms, body_ptr, body_max,
//                    addr_ptr, addr_max) → i64
//
// Reads one packet. Returns a packed i64:
//   high 32 = body bytes written (or -1 / -2 sentinel as int32)
//   low  32 = address-string bytes written (host:port form)
// On error / timeout, body sentinel is set; caller can ignore the
// addr buffer. -2 = timeout (recoverable), -1 = error.
func registerNetRecvfromImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			handle, timeoutMs, bodyPtr, bodyMax, addrPtr, addrMax int32,
		) int64 {
			lst, ok := lookupNetListener(rt, uint32(handle))
			if !ok || lst.kind != "udp" || lst.pc == nil {
				return packRecvResult(-1, 0)
			}
			timeout := time.Duration(timeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = netAcceptDefaultTimeout
			}
			if timeout > netAcceptMaxTimeout {
				timeout = netAcceptMaxTimeout
			}
			_ = lst.pc.SetReadDeadline(time.Now().Add(timeout))
			defer lst.pc.SetReadDeadline(time.Time{})

			buf := make([]byte, bodyMax)
			n, peer, err := lst.pc.ReadFrom(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					return packRecvResult(-2, 0)
				}
				return packRecvResult(-1, 0)
			}
			if !mod.Memory().Write(uint32(bodyPtr), buf[:n]) {
				return packRecvResult(-1, 0)
			}
			addrStr := peer.String()
			if int32(len(addrStr)) > addrMax {
				addrStr = addrStr[:addrMax]
			}
			if !mod.Memory().Write(uint32(addrPtr), []byte(addrStr)) {
				return packRecvResult(-1, 0)
			}
			return packRecvResult(int32(n), int32(len(addrStr)))
		}).
		Export("stado_net_recvfrom")
}

// packRecvResult packs (body_len_or_sentinel, addr_len) into the i64
// return shape: high 32 = body, low 32 = addr. The wasm caller
// extracts body via int32(ret >> 32) (signed cast preserves the
// -1/-2 sentinels) and addr via uint32(ret & 0xFFFFFFFF).
func packRecvResult(bodyLen, addrLen int32) int64 {
	return (int64(bodyLen) << 32) | (int64(addrLen) & 0xFFFFFFFF)
}

// stado_net_setopt(lst_handle, key_ptr, key_len, value_ptr, value_len) → i32
//
// Key-based dispatch for socket options on a UDP listener handle.
// Returns 0 on success, -1 on cap-denied / unknown key / unknown
// handle / underlying syscall failure.
//
// Initial keys (EP-0038i):
//
//   "broadcast"            → "true"/"false" — toggle SO_BROADCAST.
//                            Required for sendto to broadcast addrs
//                            (255.255.255.255 or subnet broadcasts).
//   "multicast_join"       → "<group_ip>[,<iface_name>]" — join the
//                            multicast group on the named interface
//                            (default any).
//   "multicast_leave"      → "<group_ip>[,<iface_name>]"
//   "multicast_loopback"   → "true"/"false" — whether multicast we
//                            send is looped back to us.
//   "multicast_ttl"        → "<int 0..255>" — TTL/hop-limit on
//                            outgoing multicast packets.
//
// All five keys require net:multicast:udp in the manifest. The
// listener must be UDP (net.PacketConn).
func registerNetSetoptImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			handle, keyPtr, keyLen, valuePtr, valueLen int32,
		) int32 {
			lst, ok := lookupNetListener(rt, uint32(handle))
			if !ok || lst.kind != "udp" || lst.pc == nil {
				return -1
			}
			if !host.NetMulticast {
				return -1
			}
			key, ok := readMemoryString(mod, uint32(keyPtr), uint32(keyLen))
			if !ok {
				return -1
			}
			val, ok := readMemoryString(mod, uint32(valuePtr), uint32(valueLen))
			if !ok {
				return -1
			}
			if err := applyUDPSetopt(lst.pc, key, val); err != nil {
				return -1
			}
			return 0
		}).
		Export("stado_net_setopt")
}

// applyUDPSetopt routes a (key, value) to the underlying socket.
// Errors return non-nil; the import surface translates to -1.
func applyUDPSetopt(pc net.PacketConn, key, value string) error {
	switch key {
	case "broadcast":
		on, err := parseBool(value)
		if err != nil {
			return err
		}
		uc, ok := pc.(*net.UDPConn)
		if !ok {
			return errSetoptUnsupported
		}
		sc, err := uc.SyscallConn()
		if err != nil {
			return err
		}
		var setErr error
		err = sc.Control(func(fd uintptr) {
			v := 0
			if on {
				v = 1
			}
			setErr = setBroadcastFD(int(fd), v)
		})
		if err != nil {
			return err
		}
		return setErr
	case "multicast_join":
		return udpMulticastChange(pc, value, true)
	case "multicast_leave":
		return udpMulticastChange(pc, value, false)
	case "multicast_loopback":
		on, err := parseBool(value)
		if err != nil {
			return err
		}
		return udpSetMulticastLoopback(pc, on)
	case "multicast_ttl":
		ttl, err := strconv.Atoi(value)
		if err != nil || ttl < 0 || ttl > 255 {
			return fmt.Errorf("multicast_ttl: invalid value %q", value)
		}
		return udpSetMulticastTTL(pc, ttl)
	default:
		return fmt.Errorf("setopt: unknown key %q", key)
	}
}

// errSetoptUnsupported is returned when the underlying conn isn't a
// *net.UDPConn (e.g. test stubs) — gives a stable sentinel for tests.
var errSetoptUnsupported = fmt.Errorf("setopt: underlying conn does not support socket options")

// parseBool accepts "true"/"false"/"1"/"0".
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected true/false; got %q", s)
}

// closeAllNetListeners reaps listener handles on Runtime.Close. Removes
// any unix socket files left behind.
func (r *Runtime) closeAllNetListeners(ctx context.Context) {
	r.handles.mu.Lock()
	listeners := make([]*netListener, 0)
	for id, e := range r.handles.entries {
		if e.typeTag != string(HandleTypeListen) {
			continue
		}
		if l, ok := e.value.(*netListener); ok {
			listeners = append(listeners, l)
		}
		delete(r.handles.entries, id)
	}
	r.handles.mu.Unlock()
	for _, l := range listeners {
		_ = l.closeUnderlying()
		if l.kind == "unix" && l.path != "" {
			_ = removeUnixSocketFile(l.path)
		}
	}
}

// removeUnixSocketFile is a thin wrapper over os.Remove that swallows
// not-found errors so listener cleanup is idempotent. Returns the
// underlying error for any other failure.
func removeUnixSocketFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
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
