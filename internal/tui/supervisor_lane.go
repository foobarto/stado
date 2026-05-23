package tui

import (
	"fmt"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

// providerLookup builds a provider by name from cfg. Indirection via a
// function value lets [resolveSupervisorLane] be unit-tested without
// touching [buildProviderByName]'s network / config / preset machinery.
// Production callers always pass `buildProviderByName`.
type providerLookup func(cfg *config.Config, name string) (agent.Provider, error)

// resolveSupervisorLane returns the provider + model to use for a
// supervisor-lane call (today: BTW; future: any input that
// [classifyInput] routes to the supervisor) when supervisor is
// enabled and configured. When supervisor is disabled / unconfigured,
// returns the fallback (worker) provider + model unchanged.
//
// When supervisor IS configured but the provider can't be built,
// returns an error. The caller MUST surface the error to the operator
// rather than silently falling back to the worker — falling back would
// defeat the documented [supervisor] trust boundary: an operator who
// pointed supervisor at a local Ollama for privacy would have the
// transcript shipped to the cloud worker provider, exactly the leak
// the config was meant to prevent.
//
// Codex finding #098 (cluster G, P0): the prior code in startBtw never
// consulted cfg.Supervisor.{Provider,Model}; every BTW question went
// to the worker regardless of supervisor config.
//
// Model selection follows the config contract documented at
// [config.Supervisor.Model] ("Empty = use the provider's default"):
//
//   - cfg.Supervisor.Model set → use it.
//   - cfg.Supervisor.Model empty → return "" so the supervisor
//     provider applies its own default. Returning the worker's model
//     here would force the supervisor to honor a model name picked
//     for a different provider, which usually fails and contradicts
//     the documented contract. Copilot caught this on round 1.
//
// Defensive guards (also from Copilot round 1):
//
//   - nil lookup → error rather than panic. Caller bug, surface it.
//   - lookup returns (nil, nil) → error rather than propagate nil.
//     Downstream callers dereference the provider for [Capabilities]
//     and [StreamTurn]; a nil here would crash there with no context.
func resolveSupervisorLane(
	cfg *config.Config,
	fallbackProvider agent.Provider,
	fallbackModel string,
	lookup providerLookup,
) (agent.Provider, string, error) {
	if cfg == nil || !cfg.Supervisor.Enabled || cfg.Supervisor.Provider == "" {
		return fallbackProvider, fallbackModel, nil
	}
	if lookup == nil {
		return nil, "", fmt.Errorf("supervisor provider %q: nil lookup (internal: caller did not pass a provider builder)", cfg.Supervisor.Provider)
	}
	p, err := lookup(cfg, cfg.Supervisor.Provider)
	if err != nil {
		return nil, "", fmt.Errorf("supervisor provider %q: %w", cfg.Supervisor.Provider, err)
	}
	if p == nil {
		return nil, "", fmt.Errorf("supervisor provider %q: lookup returned nil provider with no error", cfg.Supervisor.Provider)
	}
	return p, cfg.Supervisor.Model, nil
}

// cachedSupervisorLookup is the [providerLookup] startBtw passes to
// [resolveSupervisorLane]. It builds the supervisor provider via
// [buildProviderByName] on first call and caches the result on the
// Model so subsequent BTW calls reuse the same instance.
//
// Why caching matters: ACP- and MCP-wrapped providers
// (`internal/providers/acpwrap`, `mcpwrap`) spawn a subprocess and run
// a session handshake in their constructor. Per-call rebuild would
// fork a new subprocess for every BTW question, plus discard the
// previous session's MCP state, plus leak file descriptors. Codex P1
// caught this on #45 round 1.
//
// Build errors are NOT cached — a transient failure (e.g. ACP binary
// briefly missing) shouldn't permanently block the supervisor lane;
// next BTW retries.
func (m *Model) cachedSupervisorLookup(cfg *config.Config, name string) (agent.Provider, error) {
	m.supervisorProviderMu.Lock()
	defer m.supervisorProviderMu.Unlock()
	if m.supervisorProvider != nil {
		return m.supervisorProvider, nil
	}
	p, err := buildProviderByName(cfg, name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		// buildProviderByName's contract returns either a non-nil
		// provider or a non-nil error. Defending the contract here so
		// a future regression doesn't silently cache nil and break
		// every subsequent BTW until restart.
		return nil, fmt.Errorf("buildProviderByName(%q) returned nil with no error", name)
	}
	m.supervisorProvider = p
	return p, nil
}
