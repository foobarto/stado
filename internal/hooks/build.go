package hooks

import (
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/config"
)

// BuildLifecycleRunner constructs a LifecycleRunner from cfg.Hooks.Lifecycle,
// compiling each Lua hook in config order. A hook that fails to compile is
// SKIPPED with a warning to stderr (fail-open at build time too — a broken
// policy hook must not prevent the agent from starting); the remaining
// hooks still load. Returns a nil runner when no lifecycle hooks are
// configured, which every fire site treats as a no-op.
//
// This is the stderr-emitting convenience wrapper around
// [BuildLifecycleRunnerWithWarnings]; entry points whose alt-screen swallows
// pre-launch stderr (the TUI) should call that directly and surface the
// returned warnings in-band instead.
//
// SECURITY NOTE: cfg is expected to be the merged config with the
// project-overlay [hooks] table already stripped (config.Load does this).
// Callers must NOT pass raw project config here — Lua is a code-execution
// vector.
func BuildLifecycleRunner(cfg *config.Config) *LifecycleRunner {
	r, warnings := BuildLifecycleRunnerWithWarnings(cfg)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}
	return r
}

// BuildLifecycleRunnerWithWarnings is the pure counterpart to
// [BuildLifecycleRunner]: it builds the runner the same way but RETURNS the
// skip-warning lines instead of writing them to stderr. It is the positive
// analogue of internal/sandbox.HostUnsandboxedLines vs WarnIfHostUnsandboxed —
// an entry point that owns the screen (the alt-screen TUI, which swallows
// pre-launch stderr) can capture the warnings and render them in-band as a
// startup notice; a CLI entry point (stado run, headless) can print them to
// stderr. Keep this the single source of both paths so the text stays
// identical.
//
// A hook that fails to compile (or has no source) is SKIPPED with a warning
// in the returned slice (fail-open at build time too — a broken policy hook
// must not prevent the agent from starting); the remaining hooks still load.
// Returns (nil, nil) when no lifecycle hooks are configured.
//
// SECURITY NOTE: same as BuildLifecycleRunner — cfg must be the merged config
// with the project-overlay [hooks] table stripped.
func BuildLifecycleRunnerWithWarnings(cfg *config.Config) (*LifecycleRunner, []string) {
	if cfg == nil || len(cfg.Hooks.Lifecycle) == 0 {
		return nil, nil
	}
	var scripts []HookScript
	var warnings []string
	for i, h := range cfg.Hooks.Lifecycle {
		name := h.Name
		if name == "" {
			name = fmt.Sprintf("lifecycle[%d]", i)
		}
		src := h.Lua
		if src == "" && h.LuaFile != "" {
			data, err := os.ReadFile(h.LuaFile) //nolint:gosec // path is user/global config, not repo-controlled
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("stado: skipping lifecycle hook %q: read %s: %v", name, h.LuaFile, err))
				continue
			}
			src = string(data)
		}
		if src == "" {
			warnings = append(warnings, fmt.Sprintf("stado: skipping lifecycle hook %q: no lua/lua_file source", name))
			continue
		}
		lh, err := NewLuaHook(name, src)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("stado: skipping lifecycle hook %q: %v", name, err))
			continue
		}
		scripts = append(scripts, lh)
	}
	if len(scripts) == 0 {
		return nil, warnings
	}
	r := NewLifecycleRunner(scripts...)
	// Thread the fail-open/fail-closed posture from [hooks].fail_closed.
	// Default (false) keeps a broken policy hook from wedging the loop;
	// true turns a hook error/timeout into a deny.
	r.FailClosed = cfg.Hooks.FailClosed
	return r, warnings
}
