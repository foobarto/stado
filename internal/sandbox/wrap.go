package sandbox

// wrap.go implements the EP-0038 §I sandbox mode = "wrap" re-exec
// behaviour. When [sandbox] mode = "wrap" is set and stado has not
// already been re-exec'd under a wrapper (detected via the
// STADO_REWRAPPED env var), this package builds a wrapper invocation
// and re-execs the current binary under it.
//
// The re-exec contract:
//   - STADO_REWRAPPED=1 is set in the child environment.
//   - The child receives all original os.Args.
//   - The child exits with the wrapper process's exit code.
//
// Supported wrappers (checked in order): bwrap, then firejail.
// Falls back to NoneRunner
// with a loud warning; hard-refuses if [sandbox] refuse_no_runner = true.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RewrappedEnvVar is set to "1" inside a re-exec'd sandbox child.
// Callers check this to avoid infinite recursion.
const RewrappedEnvVar = "STADO_REWRAPPED"

// WrapConfig is the subset of [sandbox] config fields MaybeRewrap needs.
// Mirrors internal/config.Sandbox without creating an import cycle.
type WrapConfig struct {
	Mode           string   // "off" | "wrap" | "external"
	BindRO         []string // extra read-only bind mounts
	BindRW         []string // extra read-write bind mounts
	Network        string   // "host" | "namespaced" | "off"
	HTTPProxy      string
	AllowEnv       []string
	RefuseNoRunner bool
	Runner         string // "auto" | "bwrap" | "firejail"
}

// ErrAlreadyWrapped is returned when the process is already inside a sandbox.
var ErrAlreadyWrapped = errors.New("sandbox: already running inside wrapper")

// MaybeRewrap checks WrapConfig.Mode and, if mode = "wrap" and the
// process is not already wrapped, re-execs under the detected wrapper.
//
// Returns nil when:
//   - mode = "off" (no-op)
//   - the process is already wrapped (STADO_REWRAPPED=1)
//
// Returns ErrAlreadyWrapped when mode = "external" and process IS wrapped
// (caller continues normally). Returns an error with os.Exit(1) hint when
// mode = "external" but not wrapped — operator must fix their setup.
//
// On mode = "wrap" success: this function does NOT return — the process
// is replaced via exec.Command.Run(). If the wrapper exits, stado exits
// with the same code.
func MaybeRewrap(cfg WrapConfig) error {
	if cfg.Mode == "off" || cfg.Mode == "" {
		return nil
	}
	alreadyWrapped := os.Getenv(RewrappedEnvVar) == "1"

	switch cfg.Mode {
	case "external":
		if !alreadyWrapped && !looksWrapped() {
			return fmt.Errorf(
				"sandbox mode 'external' configured but stado does not appear to be " +
					"running inside a wrapper. Start stado under bwrap / firejail, " +
					"or set [sandbox] mode = \"wrap\" to have stado wrap itself")
		}
		return nil

	case "wrap":
		if alreadyWrapped {
			return nil // already re-exec'd; continue normally
		}
		return doRewrap(cfg)
	}
	return nil
}

// doRewrap builds the wrapper invocation and re-execs. Does not return
// on success.
func doRewrap(cfg WrapConfig) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("sandbox wrap: resolve self: %w", err)
	}

	runner, policyErr := pickRunner(cfg.Runner, cfg)
	if policyErr != nil {
		// Configured runner cannot enforce the requested policy
		// (e.g. firejail + bind_rw). Always abort — bypassing this via
		// RefuseNoRunner=false would silently let an unconfined process
		// inherit the operator's full filesystem + network. The
		// operator EXPLICITLY asked for confinement; refuse to lie.
		return policyErr
	}
	if runner == "" {
		msg := "Sandbox mode 'wrap' configured but no wrapper found.\n" +
			"Install bwrap (apt install bubblewrap / dnf install bubblewrap)\n" +
			"or set [sandbox] mode = \"off\" to disable sandboxing."
		if cfg.RefuseNoRunner {
			return errors.New(msg)
		}
		fmt.Fprintln(os.Stderr, "stado: warn: "+msg)
		fmt.Fprintln(os.Stderr, "stado: warn: running without process-containment sandbox.")
		return nil
	}

	args, err := buildWrapperArgs(runner, cfg, self)
	if err != nil {
		return fmt.Errorf("sandbox wrap: build args: %w", err)
	}

	// Build the child environment: pass through everything, add rewrapped marker.
	childEnv := append(os.Environ(), RewrappedEnvVar+"=1")
	if cfg.HTTPProxy != "" {
		childEnv = setEnvValue(childEnv, "HTTP_PROXY", cfg.HTTPProxy)
		childEnv = setEnvValue(childEnv, "HTTPS_PROXY", cfg.HTTPProxy)
	}
	if len(cfg.AllowEnv) > 0 {
		childEnv = filterEnv(childEnv, cfg.AllowEnv)
		// Always keep the rewrapped marker even in restricted env.
		childEnv = append(childEnv, RewrappedEnvVar+"=1")
	}

	cmd := exec.Command(args[0], append(args[1:], os.Args[1:]...)...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnv

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
	panic("unreachable")
}

// buildWrapperArgs returns the wrapper argv (not including stado's own args).
func buildWrapperArgs(runner string, cfg WrapConfig, selfPath string) ([]string, error) {
	switch runner {
	case "bwrap":
		return buildBwrapArgs(cfg, selfPath)
	case "firejail":
		return buildFirejailArgs(cfg, selfPath)
	}
	return nil, fmt.Errorf("unknown runner %q", runner)
}

func buildBwrapArgs(cfg WrapConfig, selfPath string) ([]string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, err
	}
	args := []string{
		bwrap,
		"--die-with-parent",
		"--new-session",
		"--proc", "/proc",
		"--dev", "/dev",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind-try", "/etc/ssl/certs", "/etc/ssl/certs",
		"--tmpfs", "/tmp",
	}
	// Stado data dirs — always RW.
	for _, xdgDir := range xdgStatoDirs() {
		if xdgDir != "" {
			args = append(args, "--bind", xdgDir, xdgDir)
		}
	}
	// Operator-declared binds.
	for _, p := range cfg.BindRO {
		args = append(args, "--ro-bind-try", expandHome(p), expandHome(p))
	}
	for _, p := range cfg.BindRW {
		args = append(args, "--bind-try", expandHome(p), expandHome(p))
	}
	// Network.
	switch cfg.Network {
	case "namespaced":
		args = append(args, "--unshare-net")
	case "off":
		args = append(args, "--unshare-net", "--unshare-uts")
		// "host" (default): no network flag
	}
	// Self binary.
	args = append(args, "--ro-bind", selfPath, selfPath)
	args = append(args, "--", selfPath)
	return args, nil
}

// firejailCanEnforce reports whether firejail can faithfully enforce the
// configured filesystem contract. #035: unlike bwrap, firejail has no simple
// "only these paths exist" allow-list primitive — its default is full
// filesystem visibility, and replicating bwrap's explicit mount list via
// --whitelist gymnastics is error-prone and silently under-confines when it's
// wrong. firejail CAN faithfully express read-only constraints (--read-only),
// but it cannot express "everything is hidden except this RW set". So when the
// operator declares BindRW (an explicit RW allow-list firejail can't honor) we
// treat firejail as unable to enforce the policy and fail closed elsewhere
// rather than wrapping a process that ignores the contract.
func firejailCanEnforce(cfg WrapConfig) bool {
	return len(cfg.BindRW) == 0
}

func buildFirejailArgs(cfg WrapConfig, selfPath string) ([]string, error) {
	fj, err := exec.LookPath("firejail")
	if err != nil {
		return nil, err
	}
	return firejailArgsWith(fj, cfg, selfPath)
}

// firejailArgsWith builds the firejail argv given an already-resolved binary
// path. Split from buildFirejailArgs so the #035 policy logic (fail-closed on
// unenforceable BindRW, faithful BindRO → --read-only) is unit-testable
// without firejail installed on the test host.
func firejailArgsWith(fjPath string, cfg WrapConfig, selfPath string) ([]string, error) {
	// #035: refuse to build a firejail invocation that would silently ignore
	// the configured FS allow-list. pickRunner already excludes firejail in
	// this case; this guard is defense-in-depth so no caller can route an
	// unenforceable policy through firejail and believe it's confined.
	if !firejailCanEnforce(cfg) {
		return nil, fmt.Errorf(
			"firejail cannot enforce the configured read-write bind allow-list (%d bind(s)); "+
				"install bwrap for full filesystem confinement or remove [sandbox] bind_rw", len(cfg.BindRW))
	}
	args := []string{fjPath, "--quiet"}
	if cfg.Network == "off" || cfg.Network == "namespaced" {
		args = append(args, "--net=none")
	}
	// #035: honor BindRO via --read-only, the one FS-policy primitive firejail
	// expresses faithfully. Mirrors the intent of buildBwrapArgs' --ro-bind.
	for _, p := range cfg.BindRO {
		args = append(args, "--read-only="+expandHome(p))
	}
	args = append(args, "--", selfPath)
	return args, nil
}

// pickRunner returns the first available wrapper name matching cfg.Runner,
// or an error when a configured runner cannot faithfully enforce the
// requested policy.
//
// #035 background: a runner is only acceptable if it can faithfully enforce
// the configured filesystem contract. firejail cannot express an arbitrary
// read-write allow-list (see firejailCanEnforce), so when BindRW is set
// firejail is dropped from the candidate set.
//
// Codex validated finding (post-#035): the prior version returned `""` for
// the unenforceable-policy case, which doRewrap then treated as the generic
// missing-wrapper path. With the documented default RefuseNoRunner=false,
// doRewrap printed a warning and returned nil, letting the agent continue
// COMPLETELY UNSANDBOXED despite the operator's mode="wrap" + bind_rw
// configuration — tools then ran with full host filesystem + network access.
//
// To close the fail-open, distinguish:
//
//   - "configured runner can't enforce policy" → hard error, returned
//     unconditionally so doRewrap aborts the wrap regardless of
//     RefuseNoRunner.
//   - "no runner installed at all" → empty string + nil error, which
//     doRewrap then handles per RefuseNoRunner (warn-and-continue when
//     false, hard-fail when true).
//
// The cost: an operator who pinned firejail with bind_rw on a bwrap-less
// host now gets an explicit error rather than false confinement, which is
// the correct trade for a security boundary.
func pickRunner(preference string, cfg WrapConfig) (string, error) {
	candidates := wrapperCandidates()
	canEnforce := func(c string) bool {
		if c == "firejail" {
			return firejailCanEnforce(cfg)
		}
		return true
	}
	if preference != "" && preference != "auto" {
		for _, c := range candidates {
			if c == preference {
				if !canEnforce(c) {
					return "", fmt.Errorf(
						"configured sandbox runner %q cannot enforce requested policy "+
							"(bind_rw is set, firejail has no allow-list primitive for it); "+
							"install bwrap for bind_rw confinement or remove [sandbox.wrap].bind_rw",
						c)
				}
				if _, err := exec.LookPath(c); err == nil {
					return c, nil
				}
			}
		}
		return "", nil // requested runner not available — generic missing-wrapper path
	}
	firejailInstalledButUnsafe := false
	for _, c := range candidates {
		if !canEnforce(c) {
			if c == "firejail" {
				// Copilot + Codex round 1 catch: only flag firejail
				// as unsafe if it's ACTUALLY installed. Otherwise
				// this is the generic "no wrapper found" case (the
				// host happens to be Linux so firejail is in the
				// candidate list, but it's not installed), and the
				// operator should get the missing-wrapper path
				// (warn-and-continue per RefuseNoRunner), not a
				// misleading "firejail can't enforce" error.
				if _, err := exec.LookPath(c); err == nil {
					firejailInstalledButUnsafe = true
				}
			}
			continue
		}
		if _, err := exec.LookPath(c); err == nil {
			return c, nil
		}
	}
	if firejailInstalledButUnsafe {
		// auto-mode: firejail is INSTALLED but can't enforce bind_rw,
		// and no other compatible wrapper is available. Fail closed
		// even under RefuseNoRunner=false — operator asked for
		// confinement, the only installed candidate can't deliver
		// it, refusing to silently drop the boundary.
		return "", errors.New(
			"configured sandbox policy requires bind_rw confinement that firejail cannot enforce, " +
				"and no compatible runner was found; install bwrap or remove [sandbox.wrap].bind_rw")
	}
	return "", nil
}

// looksWrapped uses heuristics to detect running inside a container/sandbox.
func looksWrapped() bool {
	// Simple heuristic: STADO_REWRAPPED already checked by caller.
	// Check for common container/sandbox env markers.
	for _, v := range []string{"container", "BWRAP_USE_SECCOMP"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

func xdgStatoDirs() []string {
	home, _ := os.UserHomeDir()
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" && home != "" {
		xdgData = home + "/.local/share"
	}
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" && home != "" {
		xdgConfig = home + "/.config"
	}
	xdgCache := os.Getenv("XDG_CACHE_HOME")
	if xdgCache == "" && home != "" {
		xdgCache = home + "/.cache"
	}
	var dirs []string
	for _, base := range []string{xdgData, xdgConfig, xdgCache} {
		if base != "" {
			dirs = append(dirs, base+"/stado")
		}
	}
	return dirs
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return home + p[1:]
		}
	}
	return p
}

// wrapperCandidates returns Linux wrapper names in preference order.
// Availability is checked via LookPath by callers.
func wrapperCandidates() []string {
	return []string{"bwrap", "firejail"}
}
