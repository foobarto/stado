//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"sync"
)

// detectList prefers bubblewrap, then falls back to None. Landlock/seccomp
// integration lands in a follow-up; PLAN.md §3.4 uses bwrap as the preferred
// exec sandbox on Linux anyway.
func detectList() []Runner {
	return []Runner{BwrapRunner{}, NoneRunner{}}
}

// BwrapRunner wraps commands in bubblewrap (bwrap). Requires the `bwrap`
// binary on PATH. Maps Policy fields to --ro-bind / --bind / --setenv /
// --unshare-net flags.
type BwrapRunner struct{}

func (BwrapRunner) Name() string    { return "bwrap" }
func (BwrapRunner) Available() bool { _, err := exec.LookPath("bwrap"); return err == nil }

func (r BwrapRunner) Command(ctx context.Context, p Policy, name string, args []string, env []string) (*exec.Cmd, error) {
	full, err := ResolveBinary(p, name)
	if err != nil {
		return nil, err
	}

	bwrapArgs := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-cgroup-try",
		"--proc", "/proc",
		"--dev", "/dev",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
	}

	if p.CWD != "" {
		bwrapArgs = append(bwrapArgs, "--bind-try", p.CWD, p.CWD, "--chdir", p.CWD)
	}
	for _, rp := range p.FSRead {
		bwrapArgs = append(bwrapArgs, "--ro-bind-try", rp, rp)
	}
	for _, wp := range p.FSWrite {
		bwrapArgs = append(bwrapArgs, "--bind-try", wp, wp)
	}

	childEnv := filterEnv(baseEnv(env), p.Env)
	cleanup := func() {}
	usePasta := false
	proxyPort := 0
	if p.Net.Kind == NetAllowHosts {
		if err := ensurePastaSpliceOnly(); err != nil {
			return nil, err
		}
		var proxy *Proxy
		proxy, err = ListenLoopback(p.Net)
		if err != nil {
			return nil, fmt.Errorf("bwrap: proxy listen: %w", err)
		}
		cleanup = func() { _ = proxy.Close() }
		for _, kv := range EnvForProxy(proxy) {
			name, value, ok := splitEnvKV(kv)
			if !ok {
				continue
			}
			childEnv = setEnvValue(childEnv, name, value)
		}
		childEnv = setEnvValue(childEnv, "NO_PROXY", "")
		childEnv = setEnvValue(childEnv, "no_proxy", "")
		tcpAddr, ok := proxy.Listener.Addr().(*net.TCPAddr)
		if !ok || tcpAddr.Port <= 0 {
			cleanup()
			return nil, fmt.Errorf("bwrap: proxy listen: unexpected addr %T %q", proxy.Listener.Addr(), proxy.Listener.Addr().String())
		}
		proxyPort = tcpAddr.Port
		usePasta = true
	}
	for _, kv := range stableEnv(childEnv) {
		name, value, ok := splitEnvKV(kv)
		if !ok {
			continue
		}
		bwrapArgs = append(bwrapArgs, "--setenv", name, value)
	}

	switch p.Net.Kind {
	case NetDenyAll:
		bwrapArgs = append(bwrapArgs, "--unshare-net")
	case NetAllowHosts:
		// Host-allowlist mode runs inside pasta's private netns, so bwrap
		// should inherit that namespace unchanged.
	case NetAllowAll:
		bwrapArgs = append(bwrapArgs, "--share-net")
	}

	bwrapArgs = append(bwrapArgs, "--", full)
	bwrapArgs = append(bwrapArgs, args...)

	cmdName := "bwrap"
	cmdArgs := bwrapArgs
	if usePasta {
		cmdName = "pasta"
		cmdArgs = []string{
			"-q",
			"-f",
			"--runas", pastaRunAs(),
			"--splice-only",
			"-t", "none",
			"-u", "none",
			"-T", strconv.Itoa(proxyPort),
			"-U", "none",
			"--",
			"bwrap",
		}
		cmdArgs = append(cmdArgs, bwrapArgs...)
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.Env = nil
	attachCleanup(ctx, cmd, cleanup)
	return cmd, nil
}

func stableEnv(env []string) []string {
	out := append([]string{}, env...)
	sort.Slice(out, func(i, j int) bool {
		ni, _, _ := splitEnvKV(out[i])
		nj, _, _ := splitEnvKV(out[j])
		if ni == nj {
			return out[i] < out[j]
		}
		return ni < nj
	})
	return out
}

func attachCleanup(ctx context.Context, cmd *exec.Cmd, cleanup func()) {
	if cleanup == nil {
		return
	}
	var once sync.Once
	runCleanup := func() { once.Do(cleanup) }
	origCancel := cmd.Cancel
	cmd.Cancel = func() error {
		runCleanup()
		if origCancel != nil {
			return origCancel()
		}
		return nil
	}
	go func() {
		<-ctx.Done()
		runCleanup()
	}()
}
