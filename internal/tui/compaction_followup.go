package tui

// Compaction follow-ups (#19): a proactive advisory when context usage
// crosses the soft threshold, and auto-recovery when a turn dies with a
// provider context-overflow error.

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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

// tryContextOverflowRecovery initiates auto-compact recovery when err is a
// context-overflow and an auto-compact plugin is installed: it compacts and
// replays the last prompt in a child session instead of dead-ending in
// stateError. Returns (cmd, true) when recovery was started.
//
// Called from BOTH error paths so the backstop is provider-universal:
// onStreamError (providers whose StreamTurn returns the error synchronously,
// e.g. oaicompat/minimax) and onStreamDone (providers that surface errors as
// EvError stream events, e.g. the Anthropic family).
func (m *Model) tryContextOverflowRecovery(err error) (tea.Cmd, bool) {
	pluginIdentity := m.autoCompactBackgroundPluginIdentity()
	if !isContextOverflowError(err) || pluginIdentity == "" {
		return nil, false
	}
	prompt := latestUserPrompt(m.msgs)
	if prompt == "" {
		return nil, false
	}
	m.state = stateIdle
	m.errorMsg = ""
	m.recoveryPrompt = prompt
	m.recoveryPluginName = pluginIdentity
	m.recoveryPluginActive = true
	m.recoveryApplicationWorker = m.applicationOwnsOperatorInput()
	m.appendBlock(block{kind: "system", body: "context overflow — running auto-compact, then replaying your last prompt in a child session."})
	m.renderBlocks()
	return m.tickBackgroundPluginsWithEvent(m.contextOverflowEvent(prompt)), true
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
	// Keep these context-window-specific — generic phrasings like "exceeds
	// the maximum" or "reduce the length" also match rate-limit / request-size
	// errors and would trigger a spurious recovery.
	for _, sig := range []string{
		"context_length_exceeded",
		"context length",
		"maximum context",
		"context window",
		"prompt is too long",
		"input is too long",
	} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}
