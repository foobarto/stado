//go:build !linux && !darwin && !windows

package runtime

import "fmt"

func terminateOwnedProcess(pid int, _ string) error {
	return fmt.Errorf("safe session termination for live pid %d is unavailable on this platform", pid)
}
