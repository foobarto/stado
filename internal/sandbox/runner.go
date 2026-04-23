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
	Command(ctx context.Context, p Policy, cmd string, args []string, env []string) (*exec.Cmd, error)
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

func (NoneRunner) Command(ctx context.Context, p Policy, name string, args []string, env []string) (*exec.Cmd, error) {
	full, err := ResolveBinary(p, name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, full, args...)
	if p.CWD != "" {
		cmd.Dir = p.CWD
	}
	cmd.Env = filterEnv(baseEnv(env), p.Env)
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

func baseEnv(env []string) []string {
	if env != nil {
		return env
	}
	return os.Environ()
}

// filterEnv drops every env var whose name isn't in keep. If the same key
// appears more than once, the last value wins.
func filterEnv(env, keep []string) []string {
	if len(keep) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, k := range keep {
		want[k] = true
	}
	last := map[string]int{}
	for i, kv := range env {
		if name, _, ok := splitEnvKV(kv); ok {
			last[name] = i
		}
	}
	var out []string
	for i, kv := range env {
		name, _, ok := splitEnvKV(kv)
		if !ok {
			continue
		}
		if want[name] && last[name] == i {
			out = append(out, kv)
		}
	}
	return out
}

func splitEnvKV(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			if i == 0 {
				return "", "", false
			}
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}

func setEnvValue(env []string, name, value string) []string {
	needle := name + "="
	for i, kv := range env {
		if len(kv) >= len(needle) && kv[:len(needle)] == needle {
			env[i] = needle + value
			return env
		}
	}
	return append(env, needle+value)
}

// GOOS is exported so tests can introspect which platform the package
// compiled against.
const GOOS = runtime.GOOS
