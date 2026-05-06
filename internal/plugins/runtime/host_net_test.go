package runtime

import (
	"context"
	"io"
	"net"
	"path/filepath"
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
