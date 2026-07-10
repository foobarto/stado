//go:build windows

package runtime

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func processIdentity(pid int) (string, error) {
	windowsPID, err := checkedWindowsPID(pid)
	if err != nil {
		return "", err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, windowsPID)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	return windowsProcessIdentity(handle)
}

func checkedWindowsPID(pid int) (uint32, error) {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("invalid Windows pid %d", pid)
	}
	return uint32(pid), nil
}

func windowsProcessIdentity(handle windows.Handle) (string, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", created.HighDateTime, created.LowDateTime), nil
}
