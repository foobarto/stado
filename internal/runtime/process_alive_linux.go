//go:build linux

package runtime

import "syscall"

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, state, err := linuxProcessStat(pid)
	if err == nil {
		return state != "Z" && state != "X"
	}
	err = syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
