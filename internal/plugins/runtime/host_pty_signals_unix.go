//go:build linux

package runtime

import "syscall"

// Register the less common Linux job-control and user signal names.
func init() {
	signalNames["USR1"] = syscall.SIGUSR1
	signalNames["USR2"] = syscall.SIGUSR2
	signalNames["STOP"] = syscall.SIGSTOP
	signalNames["CONT"] = syscall.SIGCONT
	signalNames["TSTP"] = syscall.SIGTSTP
	signalNames["WINCH"] = syscall.SIGWINCH
}
