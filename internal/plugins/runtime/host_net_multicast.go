// Multicast helpers for stado_net_setopt — split into its own file so
// the platform-specific syscall hop (SO_BROADCAST is OS-level, not
// available through Go's net.UDPConn directly) lives next to the
// x/net/ipv4 / ipv6 multicast wrapper. EP-0038i.
package runtime

import (
	"fmt"
	"net"
	"strings"
	"syscall"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// setBroadcastFD enables/disables SO_BROADCAST on a raw fd. Linux,
// BSD, and Darwin all use SOL_SOCKET / SO_BROADCAST; on Windows the
// constants are the same but we don't currently target Windows.
func setBroadcastFD(fd, value int) error {
	return syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, value)
}

// parseGroupIface splits "<group_ip>[,<iface_name>]" into the IP and
// the interface (nil = any). Returns an error for unparseable input.
func parseGroupIface(value string) (net.IP, *net.Interface, error) {
	parts := strings.SplitN(value, ",", 2)
	groupStr := strings.TrimSpace(parts[0])
	ip := net.ParseIP(groupStr)
	if ip == nil {
		return nil, nil, fmt.Errorf("multicast: invalid group IP %q", groupStr)
	}
	if !ip.IsMulticast() {
		return nil, nil, fmt.Errorf("multicast: %q is not a multicast address", groupStr)
	}
	if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" {
		return ip, nil, nil
	}
	ifaceName := strings.TrimSpace(parts[1])
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, nil, fmt.Errorf("multicast: interface %q: %w", ifaceName, err)
	}
	return ip, iface, nil
}

// udpMulticastChange joins (or leaves when join=false) a multicast
// group on the underlying *net.UDPConn. Picks the v4 or v6 wrapper
// based on the group address family.
func udpMulticastChange(pc net.PacketConn, value string, join bool) error {
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		return errSetoptUnsupported
	}
	ip, iface, err := parseGroupIface(value)
	if err != nil {
		return err
	}
	if v4 := ip.To4(); v4 != nil {
		p := ipv4.NewPacketConn(uc)
		groupAddr := &net.UDPAddr{IP: v4}
		if join {
			return p.JoinGroup(iface, groupAddr)
		}
		return p.LeaveGroup(iface, groupAddr)
	}
	p := ipv6.NewPacketConn(uc)
	groupAddr := &net.UDPAddr{IP: ip}
	if join {
		return p.JoinGroup(iface, groupAddr)
	}
	return p.LeaveGroup(iface, groupAddr)
}

// udpSetMulticastLoopback toggles the IP_MULTICAST_LOOP flag.
func udpSetMulticastLoopback(pc net.PacketConn, on bool) error {
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		return errSetoptUnsupported
	}
	// Try v4 first; if the conn is v6-only the v4 setter no-ops or
	// fails with EINVAL — fall through to v6 in that case.
	if err := ipv4.NewPacketConn(uc).SetMulticastLoopback(on); err == nil {
		return nil
	}
	return ipv6.NewPacketConn(uc).SetMulticastLoopback(on)
}

// udpSetMulticastTTL sets IP_MULTICAST_TTL (v4) and/or
// IPV6_MULTICAST_HOPS (v6).
func udpSetMulticastTTL(pc net.PacketConn, ttl int) error {
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		return errSetoptUnsupported
	}
	if err := ipv4.NewPacketConn(uc).SetMulticastTTL(ttl); err == nil {
		return nil
	}
	return ipv6.NewPacketConn(uc).SetMulticastHopLimit(ttl)
}
