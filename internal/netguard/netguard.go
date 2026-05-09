// Package netguard centralises the "is this IP/hostname private, and may
// I dial it?" policy that stado's HTTP, raw-TCP/UDP, ICMP, and DNS host
// imports all need to enforce. Before this package existed, three
// independent implementations were in flight:
//
//   - internal/httpclient/client.go guardDial: resolved once, checked
//     only addrs[0], dialed the hostname (re-resolved by net.Dialer →
//     a DNS-rebinding window). RFC1918 + loopback + link-local unicast.
//
//   - internal/plugins/runtime/host_net.go isPrivateIP: walked all
//     resolved IPs. RFC1918 + loopback + link-local unicast +
//     link-local multicast + multicast + unspecified (broader).
//
//   - internal/plugins/runtime/host_icmp.go ad-hoc check at line 132,
//     plus internal/plugins/runtime/host_dns.go guardAXFRTarget. Each
//     copy-pasted enough of host_net's check to drift from it
//     subtly.
//
// The 2026-05-09 code-quality review surfaced this as a class of bugs
// rather than three individual issues. This package is the consolidation:
// one IsPrivate (the conservative three-condition variant — RFC1918 +
// loopback + link-local unicast — used by HTTP / DNS), one
// IsPrivateBroad (adds multicast + unspecified, used by raw socket /
// ICMP contexts where reaching multicast addresses would also be
// surprising). One ResolveAndGuard that resolves a hostname AND walks
// every returned IP, returning the validated slice for the caller to
// dial. Callers that previously dialed the original hostname (and got
// a fresh DNS lookup with potentially different IPs) now dial a
// validated IP literal — closing the rebinding window.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// ErrPrivateAddress is returned by the guard helpers when an
// IP / hostname resolves to a private destination and the caller does
// not hold the corresponding "private" capability. Callers wrap this
// with a context-specific message via fmt.Errorf("...: %w", ...).
var ErrPrivateAddress = errors.New("netguard: private address blocked")

// ErrLookupFailed is returned when ResolveAndGuard cannot resolve the
// hostname at all. Callers should treat this as a hard fail, not a
// "fallback to public" — it usually indicates the destination is
// unreachable or the resolver itself is misconfigured.
var ErrLookupFailed = errors.New("netguard: hostname lookup failed")

// IsPrivate reports whether ip is in the conservative private-address
// set: RFC1918 (10/8, 172.16/12, 192.168/16) + loopback (127/8, ::1) +
// IPv6 link-local unicast (fe80::/10) + IPv4 link-local (169.254/16).
//
// This is the strict definition appropriate for HTTP / DNS / generic
// dial guards: a request to one of these addresses can leak data into
// the operator's local network or impersonate internal services. ICMP
// and raw-socket contexts that should additionally refuse multicast /
// unspecified call IsPrivateBroad instead.
//
// Returns false for nil or invalid IPs (no spurious "blocked" on bad
// input — the caller's lookup-failure path is the right place to
// surface those).
func IsPrivate(ip net.IP) bool {
	a, ok := toAddr(ip)
	if !ok {
		return false
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

// IsPrivateBroad extends IsPrivate with multicast + unspecified, for
// raw-socket / ICMP / DNS-server-target contexts where reaching
// 224.0.0.0/4 or 0.0.0.0 would also be surprising and undesirable.
// HTTP callers should use the conservative IsPrivate; reaching a
// multicast HTTP server is at worst nonsensical and at best
// non-actionable.
func IsPrivateBroad(ip net.IP) bool {
	a, ok := toAddr(ip)
	if !ok {
		return false
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified()
}

// toAddr converts a net.IP to a netip.Addr in canonical form: v4
// addresses come back as 4-byte (not v4-mapped-in-v6), so
// IsUnspecified / IsLoopback / IsPrivate report consistently regardless
// of whether the caller built the net.IP via net.ParseIP (which yields
// 16-byte v4-mapped) or net.IPv4 (which yields 4-byte). Without the
// Unmap call, "0.0.0.0" parsed via net.ParseIP would land as
// "::ffff:0.0.0.0" and IsUnspecified would return false — silently
// letting the unspecified address through guards that should refuse it.
func toAddr(ip net.IP) (netip.Addr, bool) {
	if ip == nil {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// ResolveAndGuard resolves host (a hostname or IP literal) and returns
// the validated IP slice. When allowPrivate is false, the resolver
// MUST return only public IPs — any private/loopback/link-local
// destination causes ErrPrivateAddress, even if other resolved IPs
// are public. (A multi-record DNS response with one private and one
// public IP is treated as wholly private; the caller could otherwise
// TOCTOU between resolve and dial by re-resolving.)
//
// The use rule: callers MUST dial the returned IP slice (or one of
// its members), NOT the original hostname. Dialing the hostname
// re-runs DNS resolution inside the dialer with no guard, which is
// the rebinding window the package was created to close.
//
// broad selects between IsPrivate (false) and IsPrivateBroad (true).
// HTTP/DNS callers pass false; raw-socket / ICMP callers pass true.
//
// Returns ErrLookupFailed wrapped on resolver error. Returns
// ErrPrivateAddress wrapped (with the offending IP in the message)
// when allowPrivate is false and any resolved IP is private.
func ResolveAndGuard(ctx context.Context, host string, allowPrivate, broad bool) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivate {
			if (broad && IsPrivateBroad(ip)) || (!broad && IsPrivate(ip)) {
				return nil, fmt.Errorf("%w: %s", ErrPrivateAddress, ip)
			}
		}
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrLookupFailed, host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: %s: no addresses", ErrLookupFailed, host)
	}
	if !allowPrivate {
		for _, ip := range ips {
			if (broad && IsPrivateBroad(ip)) || (!broad && IsPrivate(ip)) {
				return nil, fmt.Errorf("%w: %s resolves to %s", ErrPrivateAddress, host, ip)
			}
		}
	}
	return ips, nil
}
