//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBwrapRunnerCommand_AllowHostsSetsProxyEnv(t *testing.T) {
	if err := ensurePastaSpliceOnly(); err != nil {
		t.Skipf("pasta unavailable: %v", err)
	}
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		Exec: []string{"bash"},
		Env:  []string{"TOKEN"},
		Net: NetPolicy{
			Kind:  NetAllowHosts,
			Hosts: []string{"api.github.com"},
		},
	}, "bash", []string{"-c", "printf ignored"}, []string{
		"TOKEN=old",
		"TOKEN=override",
		"UNRELATED=drop",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	if cmd.Args[0] != "pasta" {
		t.Fatalf("command = %q, want pasta wrapper", cmd.Args[0])
	}
	if !containsArg(cmd.Args, "--splice-only") {
		t.Fatalf("args missing --splice-only: %v", cmd.Args)
	}
	if containsArg(cmd.Args, "--unshare-net") {
		t.Fatalf("args unexpectedly contain --unshare-net: %v", cmd.Args)
	}
	if containsArg(cmd.Args, "--share-net") {
		t.Fatalf("args unexpectedly contain --share-net: %v", cmd.Args)
	}

	setenv := collectSetenv(cmd.Args)
	if setenv["TOKEN"] != "override" {
		t.Fatalf("TOKEN = %q, want override", setenv["TOKEN"])
	}
	if setenv["UNRELATED"] != "" {
		t.Fatalf("UNRELATED should not be passed through, got %q", setenv["UNRELATED"])
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		value := setenv[key]
		if !strings.HasPrefix(value, "http://127.0.0.1:") {
			t.Fatalf("%s = %q, want loopback proxy", key, value)
		}
	}
	if setenv["NO_PROXY"] != "" || setenv["no_proxy"] != "" {
		t.Fatalf("NO_PROXY vars should be cleared, got NO_PROXY=%q no_proxy=%q", setenv["NO_PROXY"], setenv["no_proxy"])
	}
	proxyPort := proxyPortFromEnv(t, setenv["HTTPS_PROXY"])
	if !containsAdjacentArg(cmd.Args, "-T", proxyPort) {
		t.Fatalf("args missing pasta forwarded proxy port %q: %v", proxyPort, cmd.Args)
	}
}

func TestProbePastaSpliceOnlyRejectsOversizedHelp(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pasta")
	body := fmt.Sprintf("#!/bin/sh\nyes x | head -c %d\n", maxPastaHelpOutputBytes+1)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	err := probePastaSpliceOnly(script)
	if err == nil {
		t.Fatal("expected oversized help output error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want size rejection", err)
	}
}

func TestBwrapRunner_AllowHostsOnlyForwardsProxyPort(t *testing.T) {
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	if err := ensurePastaSpliceOnly(); err != nil {
		t.Skipf("pasta unavailable: %v", err)
	}
	// LookPath honours $PATH, which on some hosts (e.g. linuxbrew)
	// points at /home/linuxbrew/... — a path bwrap doesn't bind-mount
	// into the sandbox. Prefer /usr/bin/python3 since BwrapRunner
	// binds /usr; /bin is often a symlink to usr/bin on the host
	// but the symlink isn't bind-mounted, so /bin/python3 fails
	// execvp inside the sandbox even when it exists on the host.
	pythonBin := ""
	for _, candidate := range []string{"/usr/bin/python3", "/usr/local/bin/python3"} {
		if _, err := os.Stat(candidate); err == nil {
			pythonBin = candidate
			break
		}
	}
	if pythonBin == "" {
		resolved, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 unavailable")
		}
		pythonBin = resolved
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	blockedPort := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	script := fmt.Sprintf(`
import os, socket, urllib.parse
proxy_port = urllib.parse.urlparse(os.environ["HTTPS_PROXY"]).port
for label, port in (("proxy", proxy_port), ("blocked", %d)):
    s = socket.socket()
    s.settimeout(2)
    try:
        s.connect(("127.0.0.1", port))
        print(label, "OK")
    except Exception as e:
        print(label, "ERR", type(e).__name__)
    finally:
        s.close()
`, blockedPort)

	cmd, err := (BwrapRunner{}).Command(ctx, Policy{
		Exec: []string{pythonBin},
		Net: NetPolicy{
			Kind:  NetAllowHosts,
			Hosts: []string{"api.github.com"},
		},
	}, pythonBin, []string{"-c", script}, nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "proxy OK") {
		t.Fatalf("proxy port should be reachable, got %q", got)
	}
	if !strings.Contains(got, "blocked ERR") {
		t.Fatalf("unforwarded host loopback port should be blocked, got %q", got)
	}
}

// TestBwrapRunner_MaskSocketsEmission pins the ssh-agent-passthrough
// arg emission (decision 2026-06-13):
//   - each Mask entry emits `--tmpfs <dir>` to shadow it;
//   - the tmpfs comes AFTER the FSRead bind of the masked dir (shadow),
//     and the safe files inside it (known_hosts/config, which live in
//     FSRead) are re-bound via `--ro-bind-try` AFTER the tmpfs (restore
//     on top) — order is load-bearing: bwrap applies binds in argv order,
//     so restore-before-shadow would be clobbered;
//   - each Sockets entry emits `--bind <sock> <sock>`;
//   - SSH_AUTH_SOCK (kept by Env) is emitted via `--setenv`.
func TestBwrapRunner_MaskSocketsEmission(t *testing.T) {
	home := "/home/u"
	sshDir := filepath.Join(home, ".ssh")
	knownHosts := filepath.Join(sshDir, "known_hosts")
	sshConfig := filepath.Join(sshDir, "config")
	sock := "/run/user/1000/ssh-agent.sock"

	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		Exec:    []string{"git"},
		FSRead:  []string{home, knownHosts, sshConfig},
		Mask:    []string{sshDir},
		Sockets: []string{sock},
		Env:     []string{"SSH_AUTH_SOCK"},
		Net:     NetPolicy{Kind: NetDenyAll},
	}, "git", []string{"version"}, []string{
		"SSH_AUTH_SOCK=" + sock,
		"UNRELATED=drop",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	args := cmd.Args

	// 1. Mask emits --tmpfs for the .ssh dir.
	if !containsAdjacentArg(args, "--tmpfs", sshDir) {
		t.Fatalf("args missing --tmpfs %s: %v", sshDir, args)
	}
	// 2. Socket emits --bind <sock> <sock>.
	if !pairAt(args, "--bind", sock, sock) {
		t.Fatalf("args missing --bind %s %s: %v", sock, sock, args)
	}
	// 3. SSH_AUTH_SOCK kept via --setenv; UNRELATED dropped.
	setenv := collectSetenv(args)
	if setenv["SSH_AUTH_SOCK"] != sock {
		t.Fatalf("SSH_AUTH_SOCK = %q, want %q", setenv["SSH_AUTH_SOCK"], sock)
	}
	if setenv["UNRELATED"] != "" {
		t.Fatalf("UNRELATED should be dropped, got %q", setenv["UNRELATED"])
	}

	// 4. Order: the FSRead bind of the masked dir comes BEFORE the
	//    --tmpfs (shadow), and the restore --ro-bind-try of the safe
	//    files comes AFTER the --tmpfs (restore on top).
	tmpfsIdx := indexOfPair(args, "--tmpfs", sshDir)
	if tmpfsIdx < 0 {
		t.Fatalf("no --tmpfs %s in args", sshDir)
	}
	// The masked dir itself is bound RO before the tmpfs shadow.
	homeBind := indexOfPair(args, "--ro-bind-try", home)
	if homeBind < 0 || homeBind > tmpfsIdx {
		t.Fatalf("home FSRead bind (idx %d) must precede --tmpfs (idx %d): %v", homeBind, tmpfsIdx, args)
	}
	for _, safe := range []string{knownHosts, sshConfig} {
		restoreIdx := lastIndexOfPair(args, "--ro-bind-try", safe)
		if restoreIdx < 0 {
			t.Fatalf("safe file %s not re-bound: %v", safe, args)
		}
		if restoreIdx < tmpfsIdx {
			t.Fatalf("safe file %s restore (idx %d) must come AFTER --tmpfs (idx %d) or it gets shadowed: %v", safe, restoreIdx, tmpfsIdx, args)
		}
	}
}

// pairAt reports whether args contains flag immediately followed by a, b.
func pairAt(args []string, flag, a, b string) bool {
	for i := 0; i+2 < len(args); i++ {
		if args[i] == flag && args[i+1] == a && args[i+2] == b {
			return true
		}
	}
	return false
}

// indexOfPair returns the index of the FIRST `flag p` adjacent pair.
func indexOfPair(args []string, flag, p string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == p {
			return i
		}
	}
	return -1
}

// lastIndexOfPair returns the index of the LAST `flag p` adjacent pair.
func lastIndexOfPair(args []string, flag, p string) int {
	idx := -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == p {
			idx = i
		}
	}
	return idx
}

func collectSetenv(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i+2 < len(args); i++ {
		if args[i] != "--setenv" {
			continue
		}
		out[args[i+1]] = args[i+2]
		i += 2
	}
	return out
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsAdjacentArg(args []string, flag, want string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == want {
			return true
		}
	}
	return false
}

func proxyPortFromEnv(t *testing.T, proxyURL string) string {
	t.Helper()
	const prefix = "http://127.0.0.1:"
	if !strings.HasPrefix(proxyURL, prefix) {
		t.Fatalf("proxy URL = %q, want %q prefix", proxyURL, prefix)
	}
	return strings.TrimPrefix(proxyURL, prefix)
}
