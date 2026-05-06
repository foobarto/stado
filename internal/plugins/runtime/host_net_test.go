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
