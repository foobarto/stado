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
	cmd, err := buildSandboxedCmd(context.Background(), nil, "", []string{"true"}, nil)
	if err != nil {
		t.Fatalf("buildSandboxedCmd: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("unsandboxed one-shot cmd not detached from the controlling tty")
	}
}
