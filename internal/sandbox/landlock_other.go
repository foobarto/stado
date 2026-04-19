//go:build !linux

package sandbox

import "errors"

// ErrLandlockUnavailable on non-Linux targets is a static "not supported"
// error. ApplyLandlock is a no-op that returns it so callers can switch on
// errors.Is(err, ErrLandlockUnavailable).
var ErrLandlockUnavailable = errors.New("landlock: Linux-only")

// ApplyLandlock is a no-op on non-Linux builds; returns
// ErrLandlockUnavailable so callers treat it uniformly with old-kernel case.
func ApplyLandlock(p Policy) error {
	_ = p
	return ErrLandlockUnavailable
}
