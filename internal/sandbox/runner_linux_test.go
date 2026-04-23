//go:build linux

package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestBwrapRunnerCommand_AllowHostsSetsProxyEnv(t *testing.T) {
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

	if !containsArg(cmd.Args, "--share-net") {
		t.Fatalf("args missing --share-net: %v", cmd.Args)
	}
	if containsArg(cmd.Args, "--unshare-net") {
		t.Fatalf("args unexpectedly contain --unshare-net: %v", cmd.Args)
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
