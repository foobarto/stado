//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"net"
	"os/exec"
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

func TestBwrapRunner_AllowHostsOnlyForwardsProxyPort(t *testing.T) {
	if !(BwrapRunner{}).Available() {
		t.Skip("bwrap unavailable")
	}
	if err := ensurePastaSpliceOnly(); err != nil {
		t.Skipf("pasta unavailable: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
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
		Exec: []string{"python3"},
		Net: NetPolicy{
			Kind:  NetAllowHosts,
			Hosts: []string{"api.github.com"},
		},
	}, "python3", []string{"-c", script}, nil)
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
