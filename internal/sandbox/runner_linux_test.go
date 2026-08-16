//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	t.Cleanup(func() {
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		}
	})

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

func TestBwrapRunnerCommand_AbandonedAllowHostsCommandClosesProxy(t *testing.T) {
	if err := ensurePastaSpliceOnly(); err != nil {
		t.Skipf("pasta unavailable: %v", err)
	}
	addr := createAbandonedAllowHostsCommand(t)
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		conn, err := net.DialTimeout("tcp", addr, 25*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("command-owned cleanup did not close the abandoned allow-hosts proxy")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBwrapRunnerCommand_CompletedAllowHostsCommandClosesProxy(t *testing.T) {
	if err := ensurePastaSpliceOnly(); err != nil {
		t.Skipf("pasta unavailable: %v", err)
	}
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		Exec: []string{"true"},
		Net:  NetPolicy{Kind: NetAllowHosts, Hosts: []string{"example.com"}},
	}, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cmd.Release)
	env := collectSetenv(cmd.Args)
	addr := "127.0.0.1:" + proxyPortFromEnv(t, env["HTTPS_PROXY"])
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("proxy was not listening before command run: %v", err)
	}
	_ = conn.Close()

	if err := cmd.Run(); err != nil {
		t.Fatalf("managed command run: %v", err)
	}
	if conn, err = net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("completed managed command left its allow-hosts proxy listening")
	}
	runtime.KeepAlive(cmd) // Prove Run cleanup, not the abandonment fallback.
}

// createAbandonedAllowHostsCommand returns only the proxy address so the
// command becomes unreachable at this function boundary. Its AddCleanup owner
// must close the proxy even though a Background context never completes.
func createAbandonedAllowHostsCommand(t *testing.T) string {
	t.Helper()
	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		Exec: []string{"true"},
		Net:  NetPolicy{Kind: NetAllowHosts, Hosts: []string{"example.com"}},
	}, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := collectSetenv(cmd.Args)
	proxyURL := env["HTTPS_PROXY"]
	addr := "127.0.0.1:" + proxyPortFromEnv(t, proxyURL)
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("proxy was not listening before command abandonment: %v", err)
	}
	_ = conn.Close()
	runtime.KeepAlive(cmd)
	return addr
}

func TestBwrapRunner_CWDBindFollowsWritePolicy(t *testing.T) {
	cwd := t.TempDir()
	base := Policy{CWD: cwd, FSRead: []string{cwd}, Exec: []string{"true"}}

	readOnly, err := (BwrapRunner{}).Command(context.Background(), base, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAdjacentArg(readOnly.Args, "--ro-bind-try", cwd) {
		t.Fatalf("read-only CWD was not mounted read-only: %v", readOnly.Args)
	}
	if containsAdjacentArg(readOnly.Args, "--bind-try", cwd) {
		t.Fatalf("read-only CWD was mounted writable: %v", readOnly.Args)
	}

	base.FSWrite = []string{cwd}
	writable, err := (BwrapRunner{}).Command(context.Background(), base, "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAdjacentArg(writable.Args, "--bind-try", cwd) {
		t.Fatalf("writable CWD was not mounted writable: %v", writable.Args)
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

// TestBwrapRunner_MaskEmission pins credential-mask argument emission:
//   - each Mask entry emits `--tmpfs <dir>` to shadow it;
//   - the tmpfs comes AFTER the FSRead bind of the masked dir (shadow),
//     and the safe files inside it (known_hosts/config, which live in
//     FSRead) are re-bound via `--ro-bind-try` AFTER the tmpfs (restore
//     on top) — order is load-bearing: bwrap applies binds in argv order,
//     so restore-before-shadow would be clobbered.
func TestBwrapRunner_MaskEmission(t *testing.T) {
	home := "/home/u"
	sshDir := filepath.Join(home, ".ssh")
	knownHosts := filepath.Join(sshDir, "known_hosts")
	sshConfig := filepath.Join(sshDir, "config")

	cmd, err := (BwrapRunner{}).Command(context.Background(), Policy{
		Exec:   []string{"git"},
		FSRead: []string{home, knownHosts, sshConfig},
		Mask:   []string{sshDir},
		Net:    NetPolicy{Kind: NetDenyAll},
	}, "git", []string{"version"}, nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	args := cmd.Args
	if !containsArg(args, "--clearenv") {
		t.Fatalf("args missing --clearenv: %v", args)
	}

	// 1. Mask emits --tmpfs for the .ssh dir.
	if !containsAdjacentArg(args, "--tmpfs", sshDir) {
		t.Fatalf("args missing --tmpfs %s: %v", sshDir, args)
	}
	// 2. Order: the FSRead bind of the masked dir comes BEFORE the
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
