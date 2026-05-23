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
// Model selection:
//   - cfg.Supervisor.Model set → use it.
//   - cfg.Supervisor.Model empty → fall back to fallbackModel (the
//     worker's model). The supervisor provider may reject it as
//     "unknown model," surfacing a clear actionable error to the
//     operator instead of silent leakage.
func resolveSupervisorLane(
	cfg *config.Config,
	fallbackProvider agent.Provider,
	fallbackModel string,
	lookup providerLookup,
) (agent.Provider, string, error) {
	if cfg == nil || !cfg.Supervisor.Enabled || cfg.Supervisor.Provider == "" {
		return fallbackProvider, fallbackModel, nil
	}
	p, err := lookup(cfg, cfg.Supervisor.Provider)
	if err != nil {
		return nil, "", fmt.Errorf("supervisor provider %q: %w", cfg.Supervisor.Provider, err)
	}
	model := cfg.Supervisor.Model
	if model == "" {
		model = fallbackModel
	}
	return p, model, nil
}
