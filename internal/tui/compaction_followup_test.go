package tui

import (
	"errors"
	"testing"
)

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
