//go:build linux

package daemon

import "syscall"

// detachAttr returns SysProcAttr that detaches the spawned daemon from
// the parent's controlling terminal so it survives parent exit. Setsid
// puts the child in its own session — equivalent to the `setsid` shell
// command — which is the canonical way to background-spawn a daemon
// from a CLI on Linux.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
