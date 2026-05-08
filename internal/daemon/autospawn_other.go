//go:build !unix

package daemon

import "syscall"

// detachAttr is a no-op on non-unix platforms. Windows uses
// CREATE_NEW_PROCESS_GROUP / DETACHED_PROCESS via different fields;
// we'll add proper Windows support when the daemon ships there.
func detachAttr() *syscall.SysProcAttr { return nil }
