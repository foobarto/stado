//go:build !linux && !darwin && !windows

package runtime

import "fmt"

func processIdentity(pid int) (string, error) {
	return "", fmt.Errorf("process creation identity is unavailable for pid %d on this platform", pid)
}
