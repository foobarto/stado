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
// SECURITY NOTE: cfg is expected to be the merged config with the
// project-overlay [hooks] table already stripped (config.Load does this).
// Callers must NOT pass raw project config here — Lua is a code-execution
// vector.
func BuildLifecycleRunner(cfg *config.Config) *LifecycleRunner {
	if cfg == nil || len(cfg.Hooks.Lifecycle) == 0 {
		return nil
	}
	var scripts []HookScript
	for i, h := range cfg.Hooks.Lifecycle {
		name := h.Name
		if name == "" {
			name = fmt.Sprintf("lifecycle[%d]", i)
		}
		src := h.Lua
		if src == "" && h.LuaFile != "" {
			data, err := os.ReadFile(h.LuaFile) //nolint:gosec // path is user/global config, not repo-controlled
			if err != nil {
				fmt.Fprintf(os.Stderr, "stado: skipping lifecycle hook %q: read %s: %v\n", name, h.LuaFile, err)
				continue
			}
			src = string(data)
		}
		if src == "" {
			fmt.Fprintf(os.Stderr, "stado: skipping lifecycle hook %q: no lua/lua_file source\n", name)
			continue
		}
		lh, err := NewLuaHook(name, src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stado: skipping lifecycle hook %q: %v\n", name, err)
			continue
		}
		scripts = append(scripts, lh)
	}
	if len(scripts) == 0 {
		return nil
	}
	r := NewLifecycleRunner(scripts...)
	// Thread the fail-open/fail-closed posture from [hooks].fail_closed.
	// Default (false) keeps a broken policy hook from wedging the loop;
	// true turns a hook error/timeout into a deny.
	r.FailClosed = cfg.Hooks.FailClosed
	return r
}
