//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func seccompTestShell(t *testing.T) string {
	t.Helper()
	for _, c := range []string{"/usr/bin/sh", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	resolved, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}
	return resolved
}

// TestBwrapRunner_SeccompFilterWired (EP-0005): BwrapRunner now hands the
// compiled deny-list to bwrap via --seccomp <fd>. This proves the wiring
// end-to-end against the real runner: the --seccomp arg is present, an fd is
// attached via ExtraFiles, and that fd's bytes are EXACTLY the compiled
// DefaultKillSyscalls deny-list (not empty / not a no-op). The BPF's
// kill semantics are covered separately by the CompileDenyList unit tests.
func TestBwrapRunner_SeccompFilterWired(t *testing.T) {
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	sh := seccompTestShell(t)
	cmd, err := (BwrapRunner{}).Command(context.Background(),
		Policy{Exec: []string{sh}}, sh, []string{"-c", "true"}, []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatalf("Command build failed: %v", err)
	}
	if !hasArg(cmd.Args, "--seccomp") {
		t.Errorf("--seccomp not passed to bwrap; args=%v", cmd.Args)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("expected exactly one ExtraFiles entry (the seccomp fd); got %d", len(cmd.ExtraFiles))
	}
	got, err := io.ReadAll(cmd.ExtraFiles[0])
	if err != nil {
		t.Fatalf("read seccomp fd: %v", err)
	}
	want, err := CompileDenyList(DefaultKillSyscalls)
	if err != nil {
		t.Fatalf("CompileDenyList: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("seccomp fd is empty — no filter would be enforced")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("seccomp fd content (%d bytes) != compiled DefaultKillSyscalls (%d bytes)", len(got), len(want))
	}
}

// TestBwrapRunner_SeccompDoesNotBreakExecution (EP-0005): a benign command must
// still run successfully WITH the filter loaded — which also proves bwrap
// accepted and applied the BPF program (a malformed filter or unreadable fd
// makes bwrap abort before exec).
func TestBwrapRunner_SeccompDoesNotBreakExecution(t *testing.T) {
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	sh := seccompTestShell(t)
	cmd, err := (BwrapRunner{}).Command(context.Background(),
		Policy{Exec: []string{sh}}, sh, []string{"-c", "echo seccomp-ok"}, []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatalf("Command build failed: %v", err)
	}
	out, rerr := cmd.CombinedOutput()
	if rerr != nil {
		s := string(out)
		// Distinguish "no user namespace on this host" (skip) from a real
		// seccomp/exec breakage (fail).
		if strings.Contains(s, "namespace") || strings.Contains(s, "uid map") ||
			strings.Contains(s, "Operation not permitted") || strings.Contains(s, "clone") {
			t.Skipf("bwrap cannot create a namespace on this host: %v\n%s", rerr, s)
		}
		t.Fatalf("benign command under seccomp failed (filter may be malformed): %v\n%s", rerr, s)
	}
	if !strings.Contains(string(out), "seccomp-ok") {
		t.Errorf("unexpected output under seccomp: %q", out)
	}
}

func TestBwrapRunner_ClearsSSHAgentEnvironment(t *testing.T) {
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	sh := seccompTestShell(t)
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		Exec: []string{sh},
		Env:  []string{"PATH", "KEEP", "SSH_AUTH_SOCK", "SSH_AGENT_PID"},
		Net:  NetPolicy{Kind: NetAllowAll},
	}, sh, []string{"-c", `[ -z "${SSH_AUTH_SOCK+x}" ] && [ -z "${SSH_AGENT_PID+x}" ] && [ "$KEEP" = yes ]`}, []string{
		"PATH=/usr/bin:/bin",
		"KEEP=yes",
		"SSH_AUTH_SOCK=/tmp/ssh-test/agent.1",
		"SSH_AGENT_PID=1234",
	})
	if err != nil {
		t.Fatalf("Command build failed: %v", err)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		s := string(out)
		if strings.Contains(s, "namespace") || strings.Contains(s, "uid map") ||
			strings.Contains(s, "Operation not permitted") || strings.Contains(s, "clone") {
			t.Skipf("bwrap cannot create a namespace on this host: %v\n%s", runErr, s)
		}
		t.Fatalf("SSH-agent environment reached the sandbox or filtered env was lost: %v\n%s", runErr, s)
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
