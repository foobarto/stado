package palette

import (
	"strings"
	"sync"
)

// dynamicCommands holds slash commands registered at runtime — today
// skill-declared shortcuts (each skill's `slash:` frontmatter), tomorrow
// provider-creds (`/provider`-family) and anything else that wants to
// surface in the discovery palette without being baked into the static
// Commands list. They are merged AFTER the built-ins so a built-in always
// wins the first-seen group ordering, and they render in registration
// order within their own group.
//
// Guarded by a mutex because RegisterDynamicCommands runs at NewModel
// build-time and again on /reload (different goroutine than the render
// loop is acceptable, but the slice swap must be atomic w.r.t. readers).
var (
	dynamicMu       sync.RWMutex
	dynamicCommands []Command
)

// RegisterDynamicCommands REPLACES the dynamic command layer with cmds.
// It is a full replace (not an append) so callers that rebuild their
// command set — e.g. the TUI re-deriving skill shortcuts on /reload —
// don't accumulate stale entries. Pass nil (or an empty slice) to clear
// the dynamic layer.
//
// Collision rejection against built-ins is the CALLER's responsibility
// via CheckSlashCollision before registering; this function trusts its
// input so it stays a cheap, allocation-light swap on the render path's
// sibling goroutine.
func RegisterDynamicCommands(cmds []Command) {
	dynamicMu.Lock()
	defer dynamicMu.Unlock()
	if len(cmds) == 0 {
		dynamicCommands = nil
		return
	}
	dynamicCommands = append([]Command(nil), cmds...)
}

// DynamicCommands returns a copy of the current dynamic layer. Mainly for
// tests / introspection; the merged view used by the palette is
// allCommands().
func DynamicCommands() []Command {
	dynamicMu.RLock()
	defer dynamicMu.RUnlock()
	return append([]Command(nil), dynamicCommands...)
}

// allCommands returns the built-in Commands followed by the dynamically
// registered ones. This is the single source of truth the Model consults
// when building its match list, so dynamic commands appear in BOTH the
// Ctrl+P modal (View) and the inline "/" popup (InlineView) — they share
// one Model and one match list.
func allCommands() []Command {
	dynamicMu.RLock()
	defer dynamicMu.RUnlock()
	if len(dynamicCommands) == 0 {
		// Common case (no dynamic shortcuts): hand back the built-in
		// slice directly — refresh() copies it before mutating.
		return Commands
	}
	out := make([]Command, 0, len(Commands)+len(dynamicCommands))
	out = append(out, Commands...)
	out = append(out, dynamicCommands...)
	return out
}

// builtinNames is the set of built-in palette command names (with the
// leading "/"), computed once from the static Commands list. Used by
// CheckSlashCollision so dynamic registrants can't shadow a command that
// ships in the palette.
var builtinNames = func() map[string]bool {
	m := make(map[string]bool, len(Commands))
	for _, c := range Commands {
		m[c.Name] = true
	}
	return m
}()

// CheckSlashCollision reports whether name (with OR without a leading "/")
// collides with a built-in palette command. Returns true on collision —
// the caller should reject the registration, mirroring the `/alias create`
// rejection of names that shadow built-ins.
//
// It checks only the static built-in palette layer. The TUI layer also
// guards against its broader reserved set (handleSlash branches that have
// no palette row, e.g. /quit, /cancel) via tui.IsReservedSlashName; that
// composition lives at the registration call site, not here, so the
// palette package stays free of a tui import cycle.
func CheckSlashCollision(name string) bool {
	if name == "" {
		return false
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return builtinNames[name]
}
