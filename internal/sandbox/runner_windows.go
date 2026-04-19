//go:build windows

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// detectList on Windows currently has no native sandbox runner. PLAN §3.6
// v1: emit a one-time warning and run unsandboxed. v2 will add
// job-object + restricted-token isolation.
func detectList() []Runner {
	return []Runner{WinWarnRunner{}}
}

// WinWarnRunner runs commands without OS-level sandbox enforcement on
// Windows. Emits a single stderr warning per process so operators know
// FS / net policy is not applied at the kernel boundary.
type WinWarnRunner struct{}

var winWarnOnce sync.Once

func (WinWarnRunner) Name() string    { return "windows-passthrough" }
func (WinWarnRunner) Available() bool { return true }

func (WinWarnRunner) Command(ctx context.Context, p Policy, name string, args []string) (*exec.Cmd, error) {
	winWarnOnce.Do(func() {
		fmt.Fprintln(os.Stderr,
			"stado: sandbox: Windows runs unsandboxed (FS/net policy NOT enforced). "+
				"Phase 3.6 v2 will add job-object + restricted-token isolation.")
	})
	full, err := ResolveBinary(p, name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, full, args...)
	if p.CWD != "" {
		cmd.Dir = p.CWD
	}
	cmd.Env = filterEnv(os.Environ(), p.Env)
	return cmd, nil
}
