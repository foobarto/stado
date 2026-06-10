package tui

import (
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

func withAutoCompactPlugin(m *Model) {
	m.backgroundPlugins = []*pluginRuntime.BackgroundPlugin{{
		Manifest: plugins.Manifest{Name: "auto-compact"},
	}}
}

// #19B — onStreamError auto-recovers on a context-overflow when an
// auto-compact plugin is installed; otherwise it dead-ends in stateError.

func TestOnStreamError_ContextOverflowRecovers(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, "compute the thing"))
	withAutoCompactPlugin(m)

	_, cmd := onStreamError(m, streamErrorMsg{err: errors.New("400 context_length_exceeded")})

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle after recovery", m.state)
	}
	if !m.recoveryPluginActive {
		t.Fatal("recoveryPluginActive should be set")
	}
	if m.recoveryPrompt != "compute the thing" {
		t.Fatalf("recoveryPrompt = %q, want the last user prompt", m.recoveryPrompt)
	}
	if cmd == nil {
		t.Fatal("recovery should dispatch a background-plugin tick cmd")
	}
}

func TestOnStreamError_NonOverflowDeadEnds(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, "x"))
	withAutoCompactPlugin(m)

	_, _ = onStreamError(m, streamErrorMsg{err: errors.New("rate limit exceeded")})

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError for a non-overflow error", m.state)
	}
	if m.recoveryPluginActive {
		t.Fatal("non-overflow error should not trigger recovery")
	}
}

func TestOnStreamError_OverflowNoPluginDeadEnds(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, "x"))
	// no auto-compact plugin installed

	_, _ = onStreamError(m, streamErrorMsg{err: errors.New("context_length_exceeded")})

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError when no auto-compact plugin", m.state)
	}
}

// The Anthropic-family path: a context overflow surfaces as an EvError
// stream event → stateError → onStreamDone recovers via the same helper.
func TestOnStreamDone_ContextOverflowRecovers(t *testing.T) {
	m := scenarioModel(t)
	m.msgs = append(m.msgs, agent.Text(agent.RoleUser, "do it"))
	withAutoCompactPlugin(m)
	// Simulate what the EvError handler leaves behind.
	m.state = stateError
	m.errorMsg = "input is too long for the model's context window"

	_, cmd := onStreamDone(m, streamDoneMsg{})

	if m.state != stateIdle || !m.recoveryPluginActive {
		t.Fatalf("onStreamDone should recover an EvError context overflow; state=%v active=%v", m.state, m.recoveryPluginActive)
	}
	if cmd == nil {
		t.Fatal("recovery should dispatch a tick cmd")
	}
}

// #19A — proactive soft-threshold advisory.

func TestIsContextOverflowError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("400 context_length_exceeded: too many tokens"), true},
		{errors.New("Bad request: prompt is too long for the model"), true},
		{errors.New("maximum context length is 200000 tokens"), true},
		{errors.New("input is too long"), true},
		{errors.New("rate limit exceeded"), false},
		{errors.New("connection refused"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isContextOverflowError(c.err); got != c.want {
			t.Errorf("isContextOverflowError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestContextWarning_FiresOnceAboveSoft(t *testing.T) {
	m := scenarioModel(t)
	m.ctxSoftThreshold = 0.5
	m.ctxHardThreshold = 0.9
	// Drive contextFraction over soft via usage + a known cap.
	m.provider = fakeCappedProvider{max: 1000}
	m.usage.InputTokens = 600 // 60% — over soft, under hard

	before := countSystemBlocks(m)
	m.maybeEmitContextWarning()
	if countSystemBlocks(m) != before+1 {
		t.Fatal("expected one context advisory above the soft threshold")
	}
	if !m.softThresholdWarned {
		t.Fatal("softThresholdWarned should be set after firing")
	}
	// Second call does not re-warn.
	m.maybeEmitContextWarning()
	if countSystemBlocks(m) != before+1 {
		t.Fatal("advisory should fire only once per crossing")
	}
}

func TestContextWarning_ResetsBelowSoft(t *testing.T) {
	m := scenarioModel(t)
	m.ctxSoftThreshold = 0.5
	m.ctxHardThreshold = 0.9
	m.provider = fakeCappedProvider{max: 1000}

	m.usage.InputTokens = 600
	m.maybeEmitContextWarning()
	// Drop back under soft (e.g. after a compaction).
	m.usage.InputTokens = 100
	m.maybeEmitContextWarning()
	if m.softThresholdWarned {
		t.Fatal("warned flag should reset once usage drops back under soft")
	}
}

func TestContextWarning_SilentAboveHard(t *testing.T) {
	m := scenarioModel(t)
	m.ctxSoftThreshold = 0.5
	m.ctxHardThreshold = 0.9
	m.provider = fakeCappedProvider{max: 1000}
	m.usage.InputTokens = 950 // above hard — handled by the hard gate, not here

	before := countSystemBlocks(m)
	m.maybeEmitContextWarning()
	if countSystemBlocks(m) != before {
		t.Fatal("no soft advisory above the hard threshold (the hard gate owns that)")
	}
}

func countSystemBlocks(m *Model) int {
	n := 0
	for _, b := range m.blocks {
		if b.kind == "system" {
			n++
		}
	}
	return n
}
