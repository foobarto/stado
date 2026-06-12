package keys

import (
	"fmt"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
)

// LoadOverrides applies the [keymap.bindings] config overrides onto an
// already-built registry. Each entry maps an action name (e.g.
// "sidebar_toggle") to a comma-separated key list that REPLACES that action's
// current binding (schema default or otherwise).
//
// Unknown-action policy: an action name that doesn't correspond to a real
// Action is SKIPPED (the registry is left untouched for that entry) and
// reported in the returned error. Valid overrides in the same map still apply
// — one typo must not silently drop the operator's other rebindings, and must
// not abort booting. The caller (app.go) logs the error to stderr and keeps
// running, matching the non-fatal-warning idiom used for theme / skills /
// instructions load failures.
//
// A nil config, an empty bindings map, or an entry with an empty key list is a
// no-op (the empty-value entry is ignored, not treated as "unbind").
func LoadOverrides(r *Registry, cfg *config.Config) error {
	if cfg == nil || len(cfg.Keymap.Bindings) == 0 {
		return nil
	}
	valid := validActions()
	var unknown []string
	// Iterate deterministically so the error message is stable.
	names := make([]string, 0, len(cfg.Keymap.Bindings))
	for name := range cfg.Keymap.Bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		keysCSV := strings.TrimSpace(cfg.Keymap.Bindings[name])
		if keysCSV == "" {
			continue
		}
		action := Action(name)
		if !valid[action] {
			unknown = append(unknown, name)
			continue
		}
		r.Override(action, keysCSV)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("keymap: unknown action name(s) in [keymap.bindings]: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// validActions returns the set of recognised Action values. Every real action
// carries a default binding in Defaults (the emacs base), which is the
// authoritative roster — schema deltas only override actions already present
// there.
func validActions() map[Action]bool {
	out := make(map[Action]bool, len(Defaults))
	for action := range Defaults {
		out[action] = true
	}
	return out
}
