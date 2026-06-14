//go:build unix

package runtime

import "syscall"

// Register the Unix-only signal names. These constants are undefined in the
// Windows syscall package, so they can't live in the cross-platform
// signalNames literal — but PTYs are a Unix feature, so this is where the
// agent-relevant job-control / user signals actually matter.
func init() {
	signalNames["USR1"] = syscall.SIGUSR1
	signalNames["USR2"] = syscall.SIGUSR2
	signalNames["STOP"] = syscall.SIGSTOP
	signalNames["CONT"] = syscall.SIGCONT
	signalNames["TSTP"] = syscall.SIGTSTP
	signalNames["WINCH"] = syscall.SIGWINCH
}
