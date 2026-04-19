//go:build !linux

package sandbox

import "errors"

// Stub exports so callers on non-Linux platforms can compile. All
// entry points return ErrSeccompUnsupported; the caller typically
// logs + runs without seccomp (matches the macOS / Windows "best
// effort" posture).

// ErrSeccompUnsupported is returned by every seccomp-related call on
// non-Linux platforms.
var ErrSeccompUnsupported = errors.New("seccomp: Linux-only")

// DefaultKillSyscalls is empty on non-Linux so callers can still
// iterate it without OS branching.
var DefaultKillSyscalls = []string{}

// SockFilter mirrors struct sock_filter for cross-platform code-sharing
// convenience. Has no runtime meaning outside Linux.
type SockFilter struct {
	Code uint16
	JT   uint8
	JF   uint8
	K    uint32
}

// CompileDenyList returns ErrSeccompUnsupported on non-Linux.
func CompileDenyList(_ []string) ([]byte, error) {
	return nil, ErrSeccompUnsupported
}
