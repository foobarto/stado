package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Runner enforces a Policy for exec'd subprocess invocations.
//
// In-process tool calls (read/grep/…) go through a LightGuard instead which
// checks the policy in Go without a subprocess boundary; Runner is for the
// exec-class path where bubblewrap or firejail wraps a child process.
type Runner interface {
	Name() string    // "bwrap" | "firejail" | "none"
	Available() bool // can this host use this runner?
	Command(ctx context.Context, p Policy, cmd string, args []string, env []string) (*Command, error)
}

// Command owns an exec.Cmd and any runner resources that must live exactly as
// long as that subprocess. Run, Wait, Output, and CombinedOutput release those
// resources on every exit path; Start releases them if the process cannot be
// started. Callers that use Start must eventually use Wait (or cancel the
// command's context).
//
// Raw is only for adapters whose API requires *exec.Cmd. In that case the
// command context must own a deterministic cancellation point because the
// adapter bypasses these lifecycle methods.
type Command struct {
	*exec.Cmd
	release func()
}

// WrapCommand gives an ordinary exec.Cmd the managed command shape. It is used
// by unsandboxed call sites, where there are no runner resources to release.
func WrapCommand(cmd *exec.Cmd) *Command {
	return &Command{Cmd: cmd, release: func() {}}
}

func managedCommand(cmd *exec.Cmd, release func()) *Command {
	if release == nil {
		release = func() {}
	}
	return &Command{Cmd: cmd, release: release}
}

// Start starts the subprocess and releases runner resources immediately if
// startup fails. A successful Start transfers completion ownership to Wait.
func (c *Command) Start() error {
	err := c.Cmd.Start()
	if err != nil {
		c.release()
	}
	return err
}

// Run executes the subprocess and deterministically releases runner resources.
func (c *Command) Run() error {
	defer c.release()
	return c.Cmd.Run()
}

// Wait waits for a started subprocess and deterministically releases runner
// resources regardless of its exit status.
func (c *Command) Wait() error {
	defer c.release()
	return c.Cmd.Wait()
}

// Output runs the subprocess, captures stdout, and deterministically releases
// runner resources.
func (c *Command) Output() ([]byte, error) {
	defer c.release()
	return c.Cmd.Output()
}

// CombinedOutput runs the subprocess, captures stdout and stderr, and
// deterministically releases runner resources.
func (c *Command) CombinedOutput() ([]byte, error) {
	defer c.release()
	return c.Cmd.CombinedOutput()
}

// Release closes runner-owned resources without waiting for the subprocess.
// It is safe to call more than once. Adapters that start Raw must arrange to
// call Release at their own process-completion boundary.
func (c *Command) Release() { c.release() }

// Raw exposes the underlying exec.Cmd for third-party adapters that cannot
// accept Command. Such adapters must retain and cancel the command context.
func (c *Command) Raw() *exec.Cmd { return c.Cmd }

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
// sandbox is available OR when the policy is a no-op. NoneRunner itself does
// NOT warn — the unsandboxed notice is emitted once per process by
// WarnIfHostUnsandboxed (announce.go), not per command.
type NoneRunner struct{}

func (NoneRunner) Name() string    { return "none" }
func (NoneRunner) Available() bool { return true }

func (NoneRunner) Command(ctx context.Context, p Policy, name string, args []string, env []string) (*Command, error) {
	full, err := ResolveBinary(p, name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, full, args...) // #nosec G204 -- command is resolved through sandbox policy before execution.
	if p.CWD != "" {
		cmd.Dir = p.CWD
	}
	cmd.Env = filterEnv(baseEnv(env), p.Env)
	return WrapCommand(cmd), nil
}

// Denied is the error returned when a policy forbids the requested operation.
type Denied struct {
	Reason string
}

func (d Denied) Error() string { return "sandbox: denied: " + d.Reason }

// ResolveBinary looks up `name` on PATH and returns the absolute path.
//
// Exec list semantics (post-2026-05-09 host-as-ceiling fix):
//
//   - Exec == nil: no restriction (no policy specified for exec).
//   - Exec is non-nil but empty (`[]`): explicit deny-all. Caused by
//     a host policy intersection where host had a non-empty allow-
//     list but no overlap with guest's request — codex caught the
//     prior `len(p.Exec) > 0` gate inverting that case to allow-all.
//   - Exec is non-empty: only listed binaries allowed.
//
// The pre-fix gate `len(Exec) > 0` couldn't distinguish "nil = no
// policy" from "[] = deny all," so the intersection-shrunk-to-empty
// case bypassed all enforcement. Now the gate is `Exec != nil`,
// which handles both meaningfully.
func ResolveBinary(p Policy, name string) (string, error) {
	if p.Exec != nil {
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
		// SSH-agent delegation is not a sandbox capability. Even an explicit
		// guest keep-list cannot import the host agent into a confined process.
		// An operator-selected unsandboxed execution path bypasses filtering.
		if name == "SSH_AUTH_SOCK" || name == "SSH_AGENT_PID" {
			continue
		}
		if want[name] && last[name] == i {
			out = append(out, kv)
		}
	}
	return out
}

func stripSSHAgentEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := splitEnvKV(kv)
		if !ok || name == "SSH_AUTH_SOCK" || name == "SSH_AGENT_PID" {
			continue
		}
		out = append(out, kv)
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
