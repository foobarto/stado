//go:build unix

package runtime

import (
	"context"
	"os/exec"
	"testing"
)

// TestDetachControllingTTY guards the fix for password prompts (ssh/sudo)
// garbling the TUI: a one-shot child must run in its own session with no
// controlling terminal, so opening /dev/tty fails instead of grabbing the
// host's alt-screen.
func TestDetachControllingTTY(t *testing.T) {
	cmd := exec.Command("true")
	detachControllingTTY(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("detachControllingTTY did not set Setsid (child keeps the controlling tty)")
	}
}

// TestBuildSandboxedCmdUnsandboxedDetachesTTY: the unsandboxed exec path
// (the one the operator hit with --no-sandbox) must detach the tty.
func TestBuildSandboxedCmdUnsandboxedDetachesTTY(t *testing.T) {
	cmd, err := buildSandboxedCmd(context.Background(), nil, "/tmp", []string{"/bin/true"}, nil)
	if err != nil {
		t.Fatalf("buildSandboxedCmd: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("unsandboxed one-shot cmd not detached from the controlling tty")
	}
}

// TestBuildSandboxedCmdSandboxedDetachesTTY: when a sandbox runner is available,
// the sandbox-wrapped exec path must also detach the controlling tty.
func TestBuildSandboxedCmdSandboxedDetachesTTY(t *testing.T) {
	if !hasSandboxRunner() {
		t.Skip("native sandbox runner not detected; sandboxed exec path not available on this host")
	}
	policy := &sandboxPolicy{Net: "allow"}
	cmd, err := buildSandboxedCmd(context.Background(), policy, "/tmp", []string{"/bin/true"}, nil)
	if err != nil {
		t.Fatalf("buildSandboxedCmd(sandboxed): %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("sandboxed one-shot cmd not detached from the controlling tty")
	}
}
