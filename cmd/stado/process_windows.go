//go:build windows

package main

import "os"

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
