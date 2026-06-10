//go:build unix

package runtime

import (
	"os/exec"
	"syscall"
)

// detachControllingTTY puts the child in its own session with no
// controlling terminal. A one-shot command (ssh, sudo, git over ssh, …)
// that opens /dev/tty for a password prompt would otherwise grab the
// TUI's controlling terminal — writing the prompt straight onto the
// alt-screen (garbling the display) and reading the password from the
// operator's keystrokes. The one-shot exec path is non-interactive (stdin
// is fixed, stdout/stderr are captured), so it never legitimately needs a
// tty; interactive use goes through the PTY session tools, which allocate
// their own pty. With no controlling tty, those commands fail cleanly
// ("no tty present" / public-key-only) instead of corrupting the TUI.
//
// Setsid is preserved as a merge so a sandbox runner's other SysProcAttr
// fields (Pdeathsig, Credential, …) are not clobbered.
func detachControllingTTY(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
