//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
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

func (r BwrapRunner) Command(ctx context.Context, p Policy, name string, args []string) (*exec.Cmd, error) {
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
	for _, ev := range p.Env {
		bwrapArgs = append(bwrapArgs, "--setenv", ev, fmt.Sprintf("${%s}", ev))
	}

	switch p.Net.Kind {
	case NetDenyAll:
		bwrapArgs = append(bwrapArgs, "--unshare-net")
	case NetAllowHosts:
		// v1: bwrap shares host net; per-host filtering lives at HTTP proxy
		// layer (PLAN §3.7). Emit a one-line note so users know.
		bwrapArgs = append(bwrapArgs, "--share-net")
	case NetAllowAll:
		bwrapArgs = append(bwrapArgs, "--share-net")
	}

	bwrapArgs = append(bwrapArgs, "--", full)
	bwrapArgs = append(bwrapArgs, args...)

	cmd := exec.CommandContext(ctx, "bwrap", bwrapArgs...)
	cmd.Env = filterEnv(envSlice(), p.Env)
	return cmd, nil
}

func envSlice() []string {
	// Kept tiny + indirection'd so tests can stub via a linker override if
	// we ever need to; currently a thin wrapper.
	return append([]string{}, execEnviron()...)
}

// execEnviron is os.Environ but aliased so the builder above reads as a flat
// dependency rather than pulling os into this file.
func execEnviron() []string {
	return exec.Command("").Env // nil — NoneRunner does the real environ; bwrap filters below
}

