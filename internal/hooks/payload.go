package hooks

import (
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

// NewPostTurnPayload normalizes the shared post_turn payload shape used
// across TUI, CLI, and headless surfaces.
func NewPostTurnPayload(turnIndex int, usage agent.Usage, text string, duration time.Duration) PostTurnPayload {
	excerpt := text
	if len(excerpt) > 200 {
		excerpt = excerpt[:200]
	}
	durMS := int64(0)
	if duration > 0 {
		durMS = duration.Milliseconds()
	}
	return PostTurnPayload{
		Event:       "post_turn",
		TurnIndex:   turnIndex,
		TokensIn:    usage.InputTokens,
		TokensOut:   usage.OutputTokens,
		CostUSD:     usage.CostUSD,
		TextExcerpt: excerpt,
		DurationMS:  durMS,
	}
}

// DisabledByToolConfig mirrors the TUI's "don't bypass a config that
// removed bash from the active tool set" rule for non-TUI surfaces.
func DisabledByToolConfig(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.Tools.Enabled) > 0 {
		for _, name := range cfg.Tools.Enabled {
			if name == "bash" {
				return false
			}
		}
		return true
	}
	for _, name := range cfg.Tools.Disabled {
		if name == "bash" {
			return true
		}
	}
	return false
}
