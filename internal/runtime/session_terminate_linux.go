//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func terminateOwnedProcess(pid int, expectedIdentity string) error {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("open pidfd for %d: %w", pid, err)
	}
	defer unix.Close(pidfd)

	identity, state, err := linuxProcessStat(pid)
	if err != nil {
		if probeErr := unix.PidfdSendSignal(pidfd, 0, nil, 0); errors.Is(probeErr, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("identify pidfd process %d: %w", pid, err)
	}
	if state == "Z" || state == "X" {
		return os.ErrProcessDone
	}
	if identity != expectedIdentity {
		return fmt.Errorf("session process identity mismatch for pid %d", pid)
	}
	if err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("signal pidfd process %d: %w", pid, err)
	}
	return nil
}
