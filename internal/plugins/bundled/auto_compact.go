package bundled

// auto-compact is the only background plugin currently in the bundle.
// Inventory registration only — the host-side policy (default-on
// behavior, manifest declaration, lookup) lives in
// internal/runtime/background_defaults.go.

func init() {
	RegisterModule("auto-compact", "compact",
		[]string{"session:observe", "session:read", "session:fork", "llm:invoke:30000"})
}
