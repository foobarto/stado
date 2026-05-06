package runtime

import (
	"net"
	"testing"
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
