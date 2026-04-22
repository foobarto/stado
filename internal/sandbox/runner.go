package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Runner enforces a Policy for exec'd subprocess invocations.
//
// In-process tool calls (read/grep/…) go through a LightGuard instead which
// checks the policy in Go without a subprocess boundary; Runner is for the
// exec-class path where bubblewrap / sandbox-exec / …  wrap a child process.
type Runner interface {
	Name() string    // "bwrap" | "sandbox-exec" | "none" | …
	Available() bool // can this host use this runner?
	Command(ctx context.Context, p Policy, cmd string, args []string) (*exec.Cmd, error)
}

// Detect picks the most capable Runner available on this host. Order of
// preference: platform-specific primary → lightweight fallback → NoneRunner.
func Detect() Runner {
	for _, r := range detectList() {
		if r.Available() {
			return r
		}
	}
	return NoneRunner{}
}

// NoneRunner runs commands without any sandboxing. Used when no native
// sandbox is available OR when the policy is a no-op. Emits a one-line
// warning via stderr so users know they're unsandboxed.
type NoneRunner struct{}

func (NoneRunner) Name() string    { return "none" }
func (NoneRunner) Available() bool { return true }

func (NoneRunner) Command(ctx context.Context, p Policy, name string, args []string) (*exec.Cmd, error) {
	full, err := ResolveBinary(p, name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, full, args...)
	if p.CWD != "" {
		cmd.Dir = p.CWD
	}
	cmd.Env = filterEnv(os.Environ(), p.Env)
	return cmd, nil
}

// Denied is the error returned when a policy forbids the requested operation.
type Denied struct {
	Reason string
}

func (d Denied) Error() string { return "sandbox: denied: " + d.Reason }

// ResolveBinary looks up `name` on PATH and returns the absolute path. If the
// policy's Exec allow-list is non-empty, the binary must appear there.
func ResolveBinary(p Policy, name string) (string, error) {
	if len(p.Exec) > 0 {
		allowed := false
		for _, a := range p.Exec {
			if a == name {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", Denied{Reason: fmt.Sprintf("exec %q not in allow-list", name)}
		}
	}
	full, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("sandbox: lookup %s: %w", name, err)
	}
	return full, nil
}

// filterEnv drops every env var whose name isn't in keep.
func filterEnv(env, keep []string) []string {
	if len(keep) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, k := range keep {
		want[k] = true
	}
	var out []string
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				if want[kv[:i]] {
					out = append(out, kv)
				}
				break
			}
		}
	}
	return out
}

// GOOS is exported so tests can introspect which platform the package
// compiled against.
const GOOS = runtime.GOOS
