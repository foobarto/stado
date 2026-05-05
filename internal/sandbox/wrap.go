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
// Supported wrappers (checked in order): bwrap (Linux), firejail
// (Linux fallback), sandbox-exec (macOS). Falls back to NoneRunner
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
	Runner         string // "auto" | "bwrap" | "firejail" | "sandbox-exec"
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
					"running inside a wrapper. Start stado under bwrap / firejail / sandbox-exec, " +
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

	runner := pickRunner(cfg.Runner)
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
	case "sandbox-exec":
		return buildSandboxExecArgs(cfg, selfPath)
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

func buildFirejailArgs(cfg WrapConfig, selfPath string) ([]string, error) {
	fj, err := exec.LookPath("firejail")
	if err != nil {
		return nil, err
	}
	args := []string{fj, "--quiet"}
	if cfg.Network == "off" || cfg.Network == "namespaced" {
		args = append(args, "--net=none")
	}
	args = append(args, "--", selfPath)
	return args, nil
}

func buildSandboxExecArgs(cfg WrapConfig, selfPath string) ([]string, error) {
	se, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return nil, err
	}
	// Minimal sandbox-exec profile: allow read-everywhere, write only /tmp.
	profile := `(version 1)(allow default)(deny file-write*)(allow file-write* (subpath "/tmp"))`
	args := []string{se, "-p", profile, selfPath}
	return args, nil
}

// pickRunner returns the first available wrapper name matching cfg.Runner.
func pickRunner(preference string) string {
	candidates := wrapperCandidates()
	if preference != "" && preference != "auto" {
		for _, c := range candidates {
			if c == preference {
				if _, err := exec.LookPath(c); err == nil {
					return c
				}
			}
		}
		return "" // requested runner not available
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
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

// wrapperCandidates returns wrapper binary names in preference order
// for the current platform. Availability is checked via LookPath by callers.
func wrapperCandidates() []string {
	switch GOOS {
	case "linux":
		return []string{"bwrap", "firejail"}
	case "darwin":
		return []string{"sandbox-exec"}
	default:
		return nil
	}
}

