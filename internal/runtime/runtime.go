// Package runtime is stado's UI-independent core: session lifecycle, tool
// executor wiring, and the headless agent loop. Both the TUI and the
// `stado run` headless surface compose this.
//
// PLAN.md §9.1 calls this "internal/core/runtime.go"; kept as "runtime" so the
// CLI import path reads naturally (internal/runtime.AgentLoop).
package runtime
