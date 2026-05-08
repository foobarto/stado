//go:build !linux

package daemon

import "net"

// checkPeerUID is a no-op on non-Linux platforms. macOS supports
// SO_PEERCRED equivalents via getpeereid(2) and Windows named pipes
// have their own ACL story; both are owner-restricted by the socket /
// pipe path permissions we set elsewhere, so the no-op is safe for v1.
// If we later care about belt-and-braces uid checks on those platforms
// we'll add per-platform implementations here.
func checkPeerUID(_ net.Conn) error { return nil }
