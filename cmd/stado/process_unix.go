//go:build !windows

package main

import "syscall"

// terminateProcess sends SIGTERM to pid. Unix semantics: graceful
// termination, the process can trap + shutdown cleanly.
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
