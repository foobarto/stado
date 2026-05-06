//go:build !windows

package runtime

import "syscall"

// setBroadcastFD enables/disables SO_BROADCAST on a raw fd. Linux,
// BSD, and Darwin take the fd as int.
func setBroadcastFD(fd uintptr, value int) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, value)
}
