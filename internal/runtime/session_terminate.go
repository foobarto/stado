package runtime

import "fmt"

// TerminateSessionProcess validates the creation identity stored in the
// worktree and terminates that exact process through an OS-stable reference.
// Platforms without a stable process reference fail closed.
func TerminateSessionProcess(worktreeDir string) (int, error) {
	pid, identity, err := readSessionProcessRecord(worktreeDir)
	if err != nil {
		return 0, err
	}
	if identity == "" {
		return pid, fmt.Errorf("session process %d has a legacy PID-only record", pid)
	}
	if err := terminateOwnedProcess(pid, identity); err != nil {
		return pid, err
	}
	return pid, nil
}
