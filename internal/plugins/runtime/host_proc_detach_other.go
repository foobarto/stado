//go:build !unix

package runtime

import "os/exec"

// detachControllingTTY is a no-op off Unix: there is no /dev/tty
// controlling-terminal concept for a child process to grab.
func detachControllingTTY(cmd *exec.Cmd) {}
