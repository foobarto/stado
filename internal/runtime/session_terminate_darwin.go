//go:build darwin

package runtime

import "fmt"

func terminateOwnedProcess(pid int, _ string) error {
	return fmt.Errorf("safe session termination for live pid %d is unavailable on darwin; worktree preserved", pid)
}
