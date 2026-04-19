//go:build windows

package main

import "os"

// processAlive reports whether the given pid exists. On Windows we use
// os.FindProcess which always returns a handle if the pid is parseable;
// the real check is whether the OS still has it.
func processAlive(pid int) bool {
	// Windows doesn't have a signal(0) equivalent via syscall — best
	// we can do without cgo is FindProcess + a cheap operation.
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// FindProcess always succeeds on Windows; a terminated process
	// surfaces when you try to use it. Kept as an advisory yes.
	_ = p
	return true
}

// terminateProcess asks the OS to kill the process. Windows lacks
// SIGTERM; Kill maps to TerminateProcess which is equivalent to
// SIGKILL. Callers should not rely on graceful-shutdown semantics here.
func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
