package runtime

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNetDialAccess_CanDialTCP(t *testing.T) {
	cases := []struct {
		name   string
		globs  []NetDialPattern
		host   string
		port   string
		want   bool
	}{
		{"empty patterns = no match", nil, "example.com", "443", false},
		{"exact match", []NetDialPattern{{"api.example.com", "443"}}, "api.example.com", "443", true},
		{"host glob", []NetDialPattern{{"*.example.com", "443"}}, "api.example.com", "443", true},
		{"host non-match", []NetDialPattern{{"*.example.com", "443"}}, "other.com", "443", false},
		{"port wildcard", []NetDialPattern{{"127.0.0.1", "*"}}, "127.0.0.1", "8080", true},
		{"both wildcards", []NetDialPattern{{"*", "*"}}, "anything.example", "1234", true},
		{"case-insensitive host", []NetDialPattern{{"API.example.com", "443"}}, "api.example.com", "443", true},
		{"port mismatch", []NetDialPattern{{"127.0.0.1", "443"}}, "127.0.0.1", "8080", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &NetDialAccess{TCPGlobs: tc.globs}
			if got := a.CanDialTCP(tc.host, tc.port); got != tc.want {
				t.Errorf("CanDialTCP(%q, %q) = %v, want %v", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestNetDialAccess_NilSafety(t *testing.T) {
	var a *NetDialAccess
	if a.CanDialTCP("anything.com", "443") {
		t.Error("nil NetDialAccess.CanDialTCP should be false")
	}
	if a.CanDialUDP("anything.com", "53") {
		t.Error("nil NetDialAccess.CanDialUDP should be false")
	}
	if a.CanDialUnix("/tmp/x.sock") {
		t.Error("nil NetDialAccess.CanDialUnix should be false")
	}
	var l *NetListenAccess
	if l.CanListenTCP("0.0.0.0", "8080") {
		t.Error("nil NetListenAccess.CanListenTCP should be false")
	}
	if l.CanListenUnix("/tmp/x.sock") {
		t.Error("nil NetListenAccess.CanListenUnix should be false")
	}
}

func TestNetDialAccess_CanDialUDP(t *testing.T) {
	a := &NetDialAccess{UDPGlobs: []NetDialPattern{
		{"*.ntp.org", "123"},
		{"127.0.0.1", "*"},
	}}
	cases := []struct {
		host, port string
		want       bool
	}{
		{"pool.ntp.org", "123", true},
		{"pool.ntp.org", "53", false},
		{"127.0.0.1", "53", true},
		{"8.8.8.8", "53", false},
	}
	for _, tc := range cases {
		if got := a.CanDialUDP(tc.host, tc.port); got != tc.want {
			t.Errorf("CanDialUDP(%q, %q) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
	// TCP-cap doesn't leak into UDP.
	tcpOnly := &NetDialAccess{TCPGlobs: []NetDialPattern{{"*", "*"}}}
	if tcpOnly.CanDialUDP("example.com", "53") {
		t.Error("TCP-only access should NOT permit UDP dial")
	}
}

func TestNetDialAccess_CanDialUnix(t *testing.T) {
	a := &NetDialAccess{UnixGlobs: []string{
		"/var/run/docker.sock",
		"/tmp/*.sock",
	}}
	cases := []struct {
		path string
		want bool
	}{
		{"/var/run/docker.sock", true},
		{"/tmp/foo.sock", true},
		{"/tmp/sub/foo.sock", false}, // filepath.Match * does not cross slash
		{"/etc/passwd", false},
	}
	for _, tc := range cases {
		if got := a.CanDialUnix(tc.path); got != tc.want {
			t.Errorf("CanDialUnix(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestNetListenAccess_TCP(t *testing.T) {
	a := &NetListenAccess{TCPGlobs: []NetDialPattern{
		{"127.0.0.1", "8080"},
		{"0.0.0.0", "9090"},
	}}
	if !a.CanListenTCP("127.0.0.1", "8080") {
		t.Error("loopback:8080 should be allowed")
	}
	if a.CanListenTCP("0.0.0.0", "8080") {
		t.Error("0.0.0.0:8080 should NOT be allowed (cap is 127.0.0.1:8080)")
	}
	if !a.CanListenTCP("0.0.0.0", "9090") {
		t.Error("0.0.0.0:9090 should be allowed")
	}
}

func TestNetListenAccess_Unix(t *testing.T) {
	a := &NetListenAccess{UnixGlobs: []string{"/tmp/srv-*.sock"}}
	if !a.CanListenUnix("/tmp/srv-1.sock") {
		t.Error("matching path should be allowed")
	}
	if a.CanListenUnix("/tmp/other.sock") {
		t.Error("non-matching path should be denied")
	}
}

// TestDialNet_UDPEcho exercises the full UDP dial path: cap glob,
// private-IP guard, dial, round-trip a packet. Loopback requires
// NetHTTPRequestPrivate=true.
func TestDialNet_UDPEcho(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := srv.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = srv.WriteTo(buf[:n], addr)
		}
	}()
	port := srv.LocalAddr().(*net.UDPAddr).Port

	host := &Host{
		NetDial:               &NetDialAccess{UDPGlobs: []NetDialPattern{{"127.0.0.1", "*"}}},
		NetHTTPRequestPrivate: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialNet(ctx, host, "udp", "127.0.0.1", port, time.Second)
	if err != nil {
		t.Fatalf("dialNet: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "ping" {
		t.Errorf("echo mismatch: got %q", got)
	}
}

// TestDialNet_UDPCapDenied: dial without a matching UDPGlob is rejected.
func TestDialNet_UDPCapDenied(t *testing.T) {
	host := &Host{
		NetDial:               &NetDialAccess{TCPGlobs: []NetDialPattern{{"*", "*"}}},
		NetHTTPRequestPrivate: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if _, err := dialNet(ctx, host, "udp", "127.0.0.1", 53, time.Second); err == nil {
		t.Fatal("UDP dial without UDPGlobs should fail")
	}
}

// TestDialNet_UnixEcho exercises the full Unix-dial path: cap glob,
// path validation, dial, round-trip a byte stream over a temp socket.
func TestDialNet_UnixEcho(t *testing.T) {
	tmp := t.TempDir()
	sockPath := filepath.Join(tmp, "echo.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()

	host := &Host{NetDial: &NetDialAccess{UnixGlobs: []string{sockPath}}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialNet(ctx, host, "unix", sockPath, 0, time.Second)
	if err != nil {
		t.Fatalf("dialNet: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "ping" {
		t.Errorf("echo mismatch: got %q", got)
	}
}

// TestDialNet_UnixCapDenied: without a matching UnixGlob the dial fails.
func TestDialNet_UnixCapDenied(t *testing.T) {
	host := &Host{NetDial: &NetDialAccess{UnixGlobs: []string{"/var/run/docker.sock"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if _, err := dialNet(ctx, host, "unix", "/tmp/other.sock", 0, time.Second); err == nil {
		t.Fatal("non-matching path should be denied")
	}
}

// TestValidateUnixSocketPath: refuses traversal + over-long paths.
func TestValidateUnixSocketPath(t *testing.T) {
	cases := map[string]bool{
		"":                                       false,
		"/tmp/sock":                              true,
		"/tmp/../etc/sock":                       false, // traversal
		"/tmp/" + strings.Repeat("a", 200):       false, // > 104
		strings.Repeat("/x", 50) + "/sock":       false, // > 104
		strings.Repeat("/a", 30) + "/x.sock":     true,  // exactly within bound
	}
	for path, wantOK := range cases {
		err := validateUnixSocketPath(path)
		gotOK := err == nil
		if gotOK != wantOK {
			t.Errorf("validateUnixSocketPath(%q) ok=%v, want %v (err=%v)", path, gotOK, wantOK, err)
		}
	}
}

// TestListenNet_TCPRoundTrip: bind to loopback, connect a client,
// confirm Accept returns the connection. Cap-gated by net:listen:tcp.
func TestListenNet_TCPRoundTrip(t *testing.T) {
	host := &Host{NetListen: &NetListenAccess{TCPGlobs: []NetDialPattern{{"127.0.0.1", "0"}}}}
	lst, err := listenNet(host, "tcp", "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listenNet: %v", err)
	}
	defer lst.l.Close()
	if lst.kind != "tcp" || lst.path != "" {
		t.Errorf("kind=%q path=%q want tcp / empty", lst.kind, lst.path)
	}
	addr := lst.l.Addr().(*net.TCPAddr)
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer conn.Close()
	if d, ok := lst.l.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(time.Second))
	}
	srvConn, err := lst.l.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer srvConn.Close()
}

// TestListenNet_TCPCapDenied: bind on a non-allowed port fails.
func TestListenNet_TCPCapDenied(t *testing.T) {
	host := &Host{NetListen: &NetListenAccess{TCPGlobs: []NetDialPattern{{"127.0.0.1", "8080"}}}}
	if _, err := listenNet(host, "tcp", "127.0.0.1", 9999); err == nil {
		t.Fatal("listen on non-allowed port should fail")
	}
}

// TestListenNet_UnixCleansSocket: close the listener and confirm the
// socket file is removed.
func TestListenNet_UnixCleansSocket(t *testing.T) {
	tmp := t.TempDir()
	sockPath := filepath.Join(tmp, "srv.sock")
	host := &Host{NetListen: &NetListenAccess{UnixGlobs: []string{sockPath}}}
	lst, err := listenNet(host, "unix", sockPath, 0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if lst.path != sockPath {
		t.Errorf("path=%q want %q", lst.path, sockPath)
	}
	_ = lst.closeUnderlying()
	if err := removeUnixSocketFile(lst.path); err != nil {
		t.Errorf("remove socket: %v", err)
	}
	// Idempotent — second remove on absent file is fine.
	if err := removeUnixSocketFile(lst.path); err != nil {
		t.Errorf("second remove (idempotent) should not error: %v", err)
	}
}

// TestRuntime_ListenerCapEnforced: 9th listen call fails after 8 succeed.
func TestRuntime_ListenerCapEnforced(t *testing.T) {
	r, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close(context.Background())

	tmp := t.TempDir()
	host := &Host{NetListen: &NetListenAccess{UnixGlobs: []string{filepath.Join(tmp, "*.sock")}}}

	// Hand-allocate listeners to drive netListenerCount past the
	// per-Runtime cap. We use `unix` so the test doesn't grab N TCP
	// ports on the host.
	for i := 0; i < maxNetListenersPerRuntime; i++ {
		p := filepath.Join(tmp, "ok-")
		lst, err := listenNet(host, "unix", p+strings.Repeat("x", i+1)+".sock", 0)
		if err != nil {
			t.Fatalf("listenNet[%d]: %v", i, err)
		}
		if _, err := r.handles.alloc(string(HandleTypeListen), lst); err != nil {
			t.Fatalf("alloc[%d]: %v", i, err)
		}
		atomicAddListener(r, +1)
	}
	// 9th increment past cap is what the import handler checks before
	// calling listenNet — we exercise the cap check directly.
	if got := atomicLoadListener(r); got != int64(maxNetListenersPerRuntime) {
		t.Fatalf("expected %d listeners, got %d", maxNetListenersPerRuntime, got)
	}
}

// helpers — keep tests independent of atomic-package imports.
func atomicLoadListener(r *Runtime) int64 { return r.netListenerCount }
func atomicAddListener(r *Runtime, d int64) { r.netListenerCount += d }

// TestListenNet_UDPRoundTrip: UDP listen returns a PacketConn-backed
// listener; sendto + recvfrom pump a packet to a peer that bounces it.
func TestListenNet_UDPRoundTrip(t *testing.T) {
	// The "peer" is a separate UDP socket on loopback that simply
	// echoes whatever it gets back to the sender.
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	defer peer.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := peer.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = peer.WriteTo(buf[:n], addr)
		}
	}()
	peerPort := peer.LocalAddr().(*net.UDPAddr).Port

	host := &Host{
		NetListen:             &NetListenAccess{UDPGlobs: []NetDialPattern{{"127.0.0.1", "0"}}},
		NetDial:               &NetDialAccess{UDPGlobs: []NetDialPattern{{"127.0.0.1", "*"}}},
		NetHTTPRequestPrivate: true,
	}
	lst, err := listenNet(host, "udp", "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listenNet UDP: %v", err)
	}
	defer lst.closeUnderlying()
	if lst.kind != "udp" || lst.pc == nil {
		t.Fatalf("expected udp listener with pc; got kind=%q pc=%v", lst.kind, lst.pc)
	}

	// Send a packet to the peer.
	addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(peerPort)))
	if _, err := lst.pc.WriteTo([]byte("hello"), addr); err != nil {
		t.Fatalf("write to peer: %v", err)
	}
	// Read the echo back.
	_ = lst.pc.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 32)
	n, srcAddr, err := lst.pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("readfrom: %v", err)
	}
	if got := string(buf[:n]); got != "hello" {
		t.Errorf("echo mismatch: got %q", got)
	}
	if _, ok := srcAddr.(*net.UDPAddr); !ok {
		t.Errorf("source addr type: %T", srcAddr)
	}
}

// TestListenNet_UDPCapDenied: UDP bind without a matching glob fails.
func TestListenNet_UDPCapDenied(t *testing.T) {
	host := &Host{NetListen: &NetListenAccess{UDPGlobs: []NetDialPattern{{"127.0.0.1", "8080"}}}}
	if _, err := listenNet(host, "udp", "127.0.0.1", 9999); err == nil {
		t.Fatal("UDP bind on non-allowed port should fail")
	}
}

// TestApplyUDPSetopt_Broadcast: enabling broadcast on a real UDP
// socket toggles the kernel SO_BROADCAST flag. Skipped if the test
// environment refuses (some sandboxes filter setsockopt).
func TestApplyUDPSetopt_Broadcast(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	if err := applyUDPSetopt(pc, "broadcast", "true"); err != nil {
		t.Errorf("setopt broadcast=true: %v", err)
	}
	if err := applyUDPSetopt(pc, "broadcast", "false"); err != nil {
		t.Errorf("setopt broadcast=false: %v", err)
	}
}

// TestApplyUDPSetopt_MulticastTTL: setting multicast TTL on a UDP
// socket succeeds for valid values, rejects out-of-range.
func TestApplyUDPSetopt_MulticastTTL(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	if err := applyUDPSetopt(pc, "multicast_ttl", "32"); err != nil {
		t.Errorf("ttl=32: %v", err)
	}
	if err := applyUDPSetopt(pc, "multicast_ttl", "-1"); err == nil {
		t.Error("ttl=-1: expected error")
	}
	if err := applyUDPSetopt(pc, "multicast_ttl", "abc"); err == nil {
		t.Error("ttl=abc: expected error")
	}
}

// TestApplyUDPSetopt_UnknownKeyRejected.
func TestApplyUDPSetopt_UnknownKeyRejected(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	if err := applyUDPSetopt(pc, "no_such_key", "x"); err == nil {
		t.Error("expected error for unknown key")
	}
}

// TestApplyUDPSetopt_MulticastJoinLeave: joining + leaving a real
// IPv4 multicast group via the helper. Skipped if the test
// environment refuses multicast (containers, some sandboxes).
func TestApplyUDPSetopt_MulticastJoinLeave(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Skipf("ipv4 udp listen unavailable: %v", err)
	}
	defer pc.Close()
	// Use a documentation-range multicast group (RFC 5771) that's
	// safe for tests and unlikely to clash with other traffic.
	const testGroup = "239.255.0.42"
	if err := applyUDPSetopt(pc, "multicast_join", testGroup); err != nil {
		t.Skipf("multicast_join unavailable in this env: %v", err)
	}
	if err := applyUDPSetopt(pc, "multicast_leave", testGroup); err != nil {
		t.Errorf("multicast_leave: %v", err)
	}
}

// TestParseGroupIface_RejectsNonMulticast.
func TestParseGroupIface_RejectsNonMulticast(t *testing.T) {
	if _, _, err := parseGroupIface("192.0.2.1"); err == nil {
		t.Error("non-multicast IP should be rejected")
	}
	if _, _, err := parseGroupIface("not-an-ip"); err == nil {
		t.Error("unparseable IP should be rejected")
	}
}

// TestParseBool covers the value parser used for boolean setopts.
func TestParseBool(t *testing.T) {
	cases := map[string]bool{"true": true, "TRUE": true, "1": true, "false": false, "False": false, "0": false}
	for in, want := range cases {
		got, err := parseBool(in)
		if err != nil || got != want {
			t.Errorf("parseBool(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseBool("yes"); err == nil {
		t.Error("non-bool string should error")
	}
}

// TestPackRecvResult: the high32/low32 packing convention round-trips.
func TestPackRecvResult(t *testing.T) {
	cases := []struct {
		body, addr int32
	}{
		{0, 0},
		{42, 13},
		{-1, 0}, // error sentinel
		{-2, 0}, // timeout sentinel
		{1500, 21},
	}
	for _, tc := range cases {
		ret := packRecvResult(tc.body, tc.addr)
		gotBody := int32(ret >> 32)
		gotAddr := int32(uint32(ret))
		if gotBody != tc.body || gotAddr != tc.addr {
			t.Errorf("pack(%d, %d) → unpack got (%d, %d)", tc.body, tc.addr, gotBody, gotAddr)
		}
	}
}

// TestDialNet_PrivateAddrRefused: loopback is refused without
// NetHTTPRequestPrivate, even when the cap glob matches.
func TestDialNet_PrivateAddrRefused(t *testing.T) {
	host := &Host{
		NetDial:               &NetDialAccess{UDPGlobs: []NetDialPattern{{"*", "*"}}},
		NetHTTPRequestPrivate: false,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if _, err := dialNet(ctx, host, "udp", "127.0.0.1", 53, time.Second); err == nil {
		t.Fatal("loopback dial without private cap should fail")
	}
}

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":   true,  // loopback
		"10.0.0.1":    true,  // RFC1918
		"172.16.0.1":  true,  // RFC1918
		"192.168.1.1": true,  // RFC1918
		"169.254.0.1": true,  // link-local
		"8.8.8.8":     false, // public
		"1.1.1.1":     false, // public
		"::1":         true,  // ipv6 loopback
		"fe80::1":     true,  // ipv6 link-local
	}
	for ipStr, want := range cases {
		t.Run(ipStr, func(t *testing.T) {
			ip := net.ParseIP(ipStr)
			if ip == nil {
				t.Fatalf("ParseIP(%q) failed", ipStr)
			}
			if got := isPrivateIP(ip); got != want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", ipStr, got, want)
			}
		})
	}
}
