package tui

// Compaction follow-ups (#19): a proactive advisory when context usage
// crosses the soft threshold, and auto-recovery when a turn dies with a
// provider context-overflow error.

import (
	"fmt"
	"strings"
)

// maybeEmitContextWarning fires a one-time advisory when the context is
// filling up (≥ soft threshold, < hard) so the user can /compact or fork
// before a turn dies mid-stream. Called proactively at the top of a turn.
//
// Note: this relies on the provider reporting usage; providers that report
// zero input tokens (e.g. some OpenAI-compat runners — the #18 case) won't
// trip it. The auto-recover path (onStreamError → isContextOverflowError)
// is the universal backstop for those.
func (m *Model) maybeEmitContextWarning() {
	frac := m.contextFraction()
	if frac < m.ctxSoftThreshold || m.ctxSoftThreshold <= 0 {
		m.softThresholdWarned = false // reset once we're back under soft
		return
	}
	if m.ctxHardThreshold > 0 && frac >= m.ctxHardThreshold {
		return // the hard-threshold gate already handles this loudly
	}
	if m.softThresholdWarned {
		return
	}
	m.softThresholdWarned = true
	m.appendBlock(block{kind: "system", body: fmt.Sprintf(
		"context ~%.0f%% full (soft threshold %.0f%%) — consider /compact or forking soon, before a turn hits the hard limit mid-stream.",
		100*frac, 100*m.ctxSoftThreshold)})
}

// isContextOverflowError reports whether err looks like a provider
// context-window-exceeded error (usually surfaced as an HTTP 400). The
// match is a heuristic over the common provider phrasings; a structured
// sentinel in the agent.Provider interface would be the cleaner long-term
// fix (it would require updating every provider implementation).
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, sig := range []string{
		"context_length_exceeded",
		"context length",
		"maximum context",
		"context window",
		"too many tokens",
		"prompt is too long",
		"input is too long",
		"reduce the length",
		"exceeds the maximum",
	} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}
