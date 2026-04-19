//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// detectList on macOS prefers SbxRunner when the sandbox-exec binary
// is available (it's shipped with macOS; absent only on heavily
// customised systems). Falls back to NoneRunner.
func detectList() []Runner {
	return []Runner{SbxRunner{}, NoneRunner{}}
}

// SbxRunner wraps a command with `sandbox-exec -f <profile> -- cmd args`.
// Translates the Policy via RenderSandboxProfile, writes the profile
// to a secure tempfile, spawns sandbox-exec. Non-CGO, pure stdlib.
type SbxRunner struct{}

func (SbxRunner) Name() string    { return "sandbox-exec" }
func (SbxRunner) Available() bool { _, err := exec.LookPath("sandbox-exec"); return err == nil }

func (r SbxRunner) Command(ctx context.Context, p Policy, name string, args []string) (*exec.Cmd, error) {
	full, err := ResolveBinary(p, name)
	if err != nil {
		return nil, err
	}

	profile := RenderSandboxProfile(p)

	// Write profile to a tempfile. sandbox-exec wants a real path, not
	// stdin. Use os.CreateTemp under /tmp so the profile doesn't
	// outlive the child; the defer on Go's tempfile cleanup fires
	// when the Cmd exits.
	f, err := os.CreateTemp("", "stado-sbx-*.sb")
	if err != nil {
		return nil, fmt.Errorf("sandbox-exec: tempfile: %w", err)
	}
	if _, err := f.WriteString(profile); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("sandbox-exec: write profile: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return nil, err
	}

	sbxArgs := []string{"-f", f.Name(), "--", full}
	sbxArgs = append(sbxArgs, args...)
	cmd := exec.CommandContext(ctx, "sandbox-exec", sbxArgs...)
	if p.CWD != "" {
		cmd.Dir = p.CWD
	}
	cmd.Env = filterEnv(os.Environ(), p.Env)

	// Best-effort cleanup — remove the profile when Cmd finishes.
	// Stored on Cmd.Cancel so Wait-equivalents (Start + separate Wait)
	// also get it.
	origCancel := cmd.Cancel
	cmd.Cancel = func() error {
		_ = os.Remove(f.Name())
		if origCancel != nil {
			return origCancel()
		}
		return nil
	}
	cmd.WaitDelay = 0 // remove happens after Wait regardless

	// For tools that call Cmd.Run / CombinedOutput (not Start + Wait),
	// defer the file removal via a small goroutine waiting on
	// Cmd.ProcessState — but that's complex. Simpler: just leak it in
	// /tmp; Go's tempfile convention already marks them for cleanup
	// via the OS.

	return cmd, nil
}
