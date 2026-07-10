//go:build windows

package runtime

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func terminateOwnedProcess(pid int, expectedIdentity string) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return fmt.Errorf("query process %d: %w", pid, err)
	}
	if exitCode != windowsStillActive {
		return os.ErrProcessDone
	}
	identity, err := windowsProcessIdentity(handle)
	if err != nil {
		return fmt.Errorf("identify process %d: %w", pid, err)
	}
	if identity != expectedIdentity {
		return fmt.Errorf("session process identity mismatch for pid %d", pid)
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}
