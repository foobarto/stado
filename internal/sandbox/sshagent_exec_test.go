//go:build linux

package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBwrapRunner_MaskSocketsExecution actually RUNS bwrap to prove the
// ssh-agent passthrough end-to-end — the emission test (TestBwrapRunner_
// MaskSocketsEmission) only inspects argv and so cannot catch a runtime
// ordering bug (e.g. the tmpfs clobbering the known_hosts restore, or a
// socket that argv-looks-bound but isn't reachable). A sandboxed process must:
//  1. NOT read the masked private key (no key-byte exfiltration),
//  2. STILL read the known_hosts restored on top of the mask,
//  3. see the forwarded agent socket (as a socket node), and
//  4. have SSH_AUTH_SOCK exported.
//
// Uses a TEMP key dir + a throwaway unix socket — never the real ~/.ssh.
func TestBwrapRunner_MaskSocketsExecution(t *testing.T) {
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	// bwrap binds /usr; /bin is often an unbound symlink, so /bin/sh can fail
	// execvp inside the sandbox. Prefer /usr/bin/sh (same reason as
	// TestBwrapRunner_AllowHostsOnlyForwardsProxyPort in runner_linux_test.go).
	shBin := ""
	for _, c := range []string{"/usr/bin/sh", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(c); err == nil {
			shBin = c
			break
		}
	}
	if shBin == "" {
		resolved, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("no sh available")
		}
		shBin = resolved
	}

	// bwrap may be on PATH yet unable to create a user namespace (hardened
	// kernels, nested/seccomp-restricted containers, some CI). Available()
	// only checks PATH, so probe a trivial run and SKIP (not fail) when the
	// sandbox genuinely can't be entered on this host.
	if probe, perr := (BwrapRunner{}).Command(context.Background(), Policy{Exec: []string{shBin}},
		shBin, []string{"-c", "true"}, []string{"PATH=/usr/bin:/bin"}); perr != nil {
		t.Skipf("bwrap probe build failed: %v", perr)
	} else if out, rerr := probe.CombinedOutput(); rerr != nil {
		t.Skipf("bwrap cannot create a namespace on this host: %v\n%s", rerr, out)
	}

	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(sshDir, "id_ed25519")
	knownHosts := filepath.Join(sshDir, "known_hosts")
	if err := os.WriteFile(keyFile, []byte("PRIVATE-KEY-SENTINEL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("KNOWN-HOSTS-SENTINEL\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A real unix listener so the bound node is genuinely a socket.
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// The probe runs entirely inside the sandbox. Each line is exit-0 by
	// construction so the script always exits cleanly; the assertions are on
	// the captured stdout, not the exit code.
	script := strings.Join([]string{
		`echo "KEY:$(cat "` + keyFile + `" 2>&1)"`,
		`echo "KH:$(cat "` + knownHosts + `" 2>&1)"`,
		`[ -S "$SSH_AUTH_SOCK" ] && echo "SOCK:present" || echo "SOCK:missing"`,
		`echo "ENV:$SSH_AUTH_SOCK"`,
	}, "\n")

	cmd, err := (BwrapRunner{}).Command(ctx, Policy{
		Exec:    []string{shBin},
		FSRead:  []string{home, knownHosts},
		Mask:    []string{sshDir},
		Sockets: []string{sock},
		Env:     []string{"SSH_AUTH_SOCK", "PATH"},
		CWD:     home,
		Net:     NetPolicy{Kind: NetDenyAll},
	}, shBin, []string{"-c", script}, []string{
		"SSH_AUTH_SOCK=" + sock,
		"PATH=/usr/bin:/bin",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap run: %v\n%s", err, out)
	}
	got := string(out)
	t.Logf("sandbox probe output:\n%s", got)

	// 1. The masked private key must be UNREADABLE (no key bytes leak).
	if strings.Contains(got, "PRIVATE-KEY-SENTINEL") {
		t.Errorf("masked SSH private key was readable inside the sandbox:\n%s", got)
	}
	// 2. known_hosts is restored on top of the mask (git host-key verification
	//    still works).
	if !strings.Contains(got, "KNOWN-HOSTS-SENTINEL") {
		t.Errorf("known_hosts was not restored inside the sandbox:\n%s", got)
	}
	// 3. The forwarded agent socket is present as a socket node.
	if !strings.Contains(got, "SOCK:present") {
		t.Errorf("forwarded ssh-agent socket not reachable inside the sandbox:\n%s", got)
	}
	// 4. SSH_AUTH_SOCK is exported into the sandbox.
	if !strings.Contains(got, "ENV:"+sock) {
		t.Errorf("SSH_AUTH_SOCK not exported into the sandbox:\n%s", got)
	}
}
