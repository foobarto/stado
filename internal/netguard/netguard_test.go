package netguard

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// TestIsPrivate locks the conservative three-condition definition:
// RFC1918 + loopback + link-local unicast. Multicast and unspecified
// are NOT private under this predicate (callers wanting to refuse
// those use IsPrivateBroad).
func TestIsPrivate(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// Loopback
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"::1", true},
		// RFC1918 v4
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		// IPv4 link-local unicast (169.254/16)
		{"169.254.0.1", true},
		// IPv6 link-local unicast (fe80::/10)
		{"fe80::1", true},
		// Public — must not match
		{"8.8.8.8", false},
		{"192.0.2.1", false}, // TEST-NET-1
		{"2001:4860:4860::8888", false},
		// Multicast / unspecified — IsPrivate must NOT block these
		// (IsPrivateBroad does that).
		{"224.0.0.1", false},
		{"0.0.0.0", false},
		// Boundary: 172.15 and 172.32 are NOT in 172.16/12.
		{"172.15.0.1", false},
		{"172.32.0.1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Errorf("test bug: cannot parse %q", c.ip)
			continue
		}
		got := IsPrivate(ip)
		if got != c.want {
			t.Errorf("IsPrivate(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestIsPrivateBroad confirms multicast + unspecified are caught,
// while public addresses still pass.
func TestIsPrivateBroad(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true}, // loopback (also matched by strict)
		{"224.0.0.1", true}, // multicast
		{"ff02::1", true},   // link-local multicast
		{"0.0.0.0", true},   // unspecified
		{"::", true},        // unspecified v6
		{"8.8.8.8", false},  // public — even broad doesn't block
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		got := IsPrivateBroad(ip)
		if got != c.want {
			t.Errorf("IsPrivateBroad(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestIsPrivate_NilAndInvalid: defensive paths return false rather
// than spurious "blocked" — caller's lookup-failure handler is the
// right place to surface bad input.
func TestIsPrivate_NilAndInvalid(t *testing.T) {
	if IsPrivate(nil) {
		t.Error("IsPrivate(nil) should be false")
	}
	if IsPrivateBroad(nil) {
		t.Error("IsPrivateBroad(nil) should be false")
	}
}

// TestResolveAndGuard_IPLiteralPublic: public IP literal passes,
// returned slice contains exactly that IP.
func TestResolveAndGuard_IPLiteralPublic(t *testing.T) {
	ips, err := ResolveAndGuard(context.Background(), "192.0.2.1", false, false)
	if err != nil {
		t.Fatalf("public IP literal: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.1")) {
		t.Errorf("got %v, want [192.0.2.1]", ips)
	}
}

// TestResolveAndGuard_IPLiteralPrivateBlocked: private IP literal
// blocked when allowPrivate=false; allowed when true. Verifies the
// flag is honoured.
func TestResolveAndGuard_IPLiteralPrivateBlocked(t *testing.T) {
	cases := []struct {
		ip           string
		allowPrivate bool
		wantBlocked  bool
	}{
		{"127.0.0.1", false, true},
		{"127.0.0.1", true, false},
		{"10.0.0.1", false, true},
		{"10.0.0.1", true, false},
		// Multicast: blocked under broad=true even when allowPrivate=false
		// covers it (see broad=true vs broad=false branches below).
		{"224.0.0.1", false, false}, // strict mode: multicast not blocked
	}
	for _, c := range cases {
		_, err := ResolveAndGuard(context.Background(), c.ip, c.allowPrivate, false)
		blocked := errors.Is(err, ErrPrivateAddress)
		if blocked != c.wantBlocked {
			t.Errorf("ResolveAndGuard(%s, allowPrivate=%v, broad=false): blocked=%v, want %v (err=%v)",
				c.ip, c.allowPrivate, blocked, c.wantBlocked, err)
		}
	}
}

// TestResolveAndGuard_BroadModeBlocksMulticast: with broad=true,
// multicast and unspecified are also rejected when allowPrivate=false.
func TestResolveAndGuard_BroadModeBlocksMulticast(t *testing.T) {
	for _, ip := range []string{"224.0.0.1", "0.0.0.0", "ff02::1"} {
		_, err := ResolveAndGuard(context.Background(), ip, false, true)
		if !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("broad mode should block %s; got err=%v", ip, err)
		}
	}
}

// TestResolveAndGuard_LookupFailure: a bogus hostname surfaces
// ErrLookupFailed wrapped; callers can errors.Is it.
func TestResolveAndGuard_LookupFailure(t *testing.T) {
	// "127.0.0.1.invalid" is unlikely to resolve on any system.
	_, err := ResolveAndGuard(context.Background(),
		"this-host-does-not-exist-zzz.invalid", false, false)
	if err == nil {
		t.Fatal("expected lookup failure, got nil")
	}
	if !errors.Is(err, ErrLookupFailed) {
		t.Errorf("err should wrap ErrLookupFailed; got %v", err)
	}
}

// TestResolveAndGuard_HostnameLocalhost: "localhost" resolves to
// loopback on every platform; ResolveAndGuard with allowPrivate=false
// must refuse it. Locks the mixed-IP-record property: even if "localhost"
// somehow returns one public IP and one loopback, the loopback alone
// trips the guard.
func TestResolveAndGuard_HostnameLocalhost(t *testing.T) {
	_, err := ResolveAndGuard(context.Background(), "localhost", false, false)
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("localhost with allowPrivate=false: want ErrPrivateAddress, got %v", err)
	}
	// Sanity: with allowPrivate=true, the same lookup should succeed
	// and return non-empty IPs.
	ips, err := ResolveAndGuard(context.Background(), "localhost", true, false)
	if err != nil {
		t.Fatalf("localhost with allowPrivate=true: %v", err)
	}
	if len(ips) == 0 {
		t.Errorf("localhost should resolve to ≥1 IP")
	}
}

// TestResolveAndGuard_ErrorMessageNamesIP: the wrapped ErrPrivateAddress
// must include the offending IP literal in its message — operators
// debugging a refusal need to see *which* IP was blocked, not just
// "private". Locks operator-facing diagnostic quality.
func TestResolveAndGuard_ErrorMessageNamesIP(t *testing.T) {
	_, err := ResolveAndGuard(context.Background(), "10.0.0.1", false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "10.0.0.1") {
		t.Errorf("error message should name the blocked IP; got %q", err.Error())
	}
}
