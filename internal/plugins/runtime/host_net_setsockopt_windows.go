//go:build windows

package runtime

import "syscall"

// setBroadcastFD enables/disables SO_BROADCAST on a Windows socket
// handle. The constants match POSIX (SOL_SOCKET / SO_BROADCAST), but
// SetsockoptInt's signature uses syscall.Handle on Windows.
func setBroadcastFD(fd uintptr, value int) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, value)
}
