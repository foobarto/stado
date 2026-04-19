//go:build !windows

package main

import "syscall"

// processAlive reports whether the given pid exists + is signalable by
// the current user. On Unix this is a syscall.Kill(pid, 0) probe.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// terminateProcess sends SIGTERM to pid. Unix semantics: graceful
// termination, the process can trap + shutdown cleanly.
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
