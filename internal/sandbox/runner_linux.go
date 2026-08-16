//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// detectList prefers bubblewrap, then falls back to None. Landlock/seccomp
// integration lands in a follow-up; PLAN.md §3.4 uses bwrap as the preferred
// exec sandbox on Linux anyway.
func detectList() []Runner {
	return []Runner{BwrapRunner{}, NoneRunner{}}
}

// BwrapRunner wraps commands in bubblewrap (bwrap). Requires the `bwrap`
// binary on PATH. Maps Policy fields to --ro-bind / --setenv /
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
		// bwrap otherwise inherits the complete launcher environment. Clear it
		// before adding the policy-filtered variables below.
		"--clearenv",
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
		bindMode := "--ro-bind-try"
		if resolvedPathWithinAny(p.CWD, p.FSWrite) {
			bindMode = "--bind-try"
		}
		bwrapArgs = append(bwrapArgs, bindMode, p.CWD, p.CWD, "--chdir", p.CWD)
	}
	for _, rp := range p.FSRead {
		bwrapArgs = append(bwrapArgs, "--ro-bind-try", rp, rp)
	}
	for _, wp := range p.FSWrite {
		bwrapArgs = append(bwrapArgs, "--bind-try", wp, wp)
	}

	// Mask renders a directory unreadable even though an ancestor was
	// bound RO above (e.g. HOME bound RO, but the key dir must not be
	// exfiltratable). bwrap applies operations in argv order, so the
	// shadow MUST come AFTER the FSRead binds and the restore of any
	// safe files inside the masked dir MUST come AFTER the shadow — else
	// the empty tmpfs would clobber the restore. The masked dir name is
	// supplied by the caller (constructed, never read here); this code
	// only shadows it.
	for _, mp := range p.Mask {
		bwrapArgs = append(bwrapArgs, "--tmpfs", mp)
	}
	// Restore explicitly allowed paths: any FSRead entry that lives UNDER a
	// masked dir gets re-bound on top of the tmpfs. Paths also covered by
	// FSWrite remain writable (for example a workdir below private /tmp);
	// the rest are restored read-only.
	for _, rp := range p.FSRead {
		if underAnyMask(rp, p.Mask) {
			bindMode := "--ro-bind-try"
			if resolvedPathWithinAny(rp, p.FSWrite) {
				bindMode = "--bind-try"
			}
			bwrapArgs = append(bwrapArgs, bindMode, rp, rp)
		}
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

	// EP-0005: hand the compiled seccomp deny-list to bwrap via --seccomp <fd>
	// (child fd 3 = the first ExtraFiles entry). Defense-in-depth — kills
	// mount/ptrace/reboot/... (DefaultKillSyscalls). Fail-safe: on any
	// compile/memfd error, proceed WITHOUT the filter rather than break the
	// tool call. Skipped under pasta (network-allowlist mode): fd inheritance
	// across the pasta→bwrap exec is unverified and a rejected --seccomp fd
	// would abort the command; the filter still covers the deny-net /
	// allow-all / no-net paths (the common case).
	var seccompFile *os.File
	if !usePasta {
		if f, ferr := newSeccompFilterFile(); ferr != nil {
			fmt.Fprintf(os.Stderr, "stado: warn: seccomp filter unavailable, running without it: %v\n", ferr)
		} else {
			seccompFile = f
			bwrapArgs = append(bwrapArgs, "--seccomp", "3")
		}
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
	if seccompFile != nil {
		// ExtraFiles[0] becomes child fd 3 (the --seccomp arg above). Close the
		// memfd after the process finishes (it's dup'd into the child at Start).
		cmd.ExtraFiles = []*os.File{seccompFile}
		prev := cleanup
		cleanup = func() {
			if prev != nil {
				prev()
			}
			_ = seccompFile.Close()
		}
	}
	cmd.Env = nil
	attachCleanup(ctx, cmd, cleanup)
	return cmd, nil
}

// underAnyMask reports whether path p is a strict descendant of a masked
// directory. It decides which explicit FSRead entries to re-bind on top of a
// Mask tmpfs (the "shadow then selectively restore" pattern). Equality is
// intentionally excluded: restoring an exact masked root such as /tmp would
// expose the host directory and defeat the mask. Comparison is lexical on
// cleaned paths with a trailing-separator guard, so "/a/.sshX" is not treated
// as under "/a/.ssh".
func underAnyMask(p string, masks []string) bool {
	cp := filepath.Clean(p)
	for _, m := range masks {
		cm := filepath.Clean(m)
		if strings.HasPrefix(cp, cm+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
	done := ctx.Done()
	if done == nil {
		// context.Background and context.TODO never complete. Do not pin the
		// command, proxy, or seccomp fd behind an immortal watcher goroutine;
		// cmd.Cancel still offers explicit cleanup, and otherwise the command's
		// owned files become collectible with the command.
		return
	}
	go func() {
		<-done
		runCleanup()
	}()
}
