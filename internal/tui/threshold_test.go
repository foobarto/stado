package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/foobarto/stado/pkg/agent"
)

// fakeCappedProvider exposes a fixed MaxContextTokens without network calls —
// enough for tokenPctString to compute a ratio.
type fakeCappedProvider struct {
	max int
}

func (fakeCappedProvider) Name() string { return "fake" }
func (p fakeCappedProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{MaxContextTokens: p.max}
}
func (fakeCappedProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	panic("not called in these tests")
}

// TestSetContextThresholdsRejectsInvalid asserts bad values are ignored and
// defaults survive.
func TestSetContextThresholdsRejectsInvalid(t *testing.T) {
	m := &Model{ctxSoftThreshold: 0.7, ctxHardThreshold: 0.9}

	m.SetContextThresholds(0, 0) // zero → kept
	if m.ctxSoftThreshold != 0.7 || m.ctxHardThreshold != 0.9 {
		t.Fatalf("zero passthrough: %v/%v", m.ctxSoftThreshold, m.ctxHardThreshold)
	}
	m.SetContextThresholds(-0.1, 1.5) // out-of-range → kept
	if m.ctxSoftThreshold != 0.7 || m.ctxHardThreshold != 0.9 {
		t.Fatalf("out-of-range passthrough: %v/%v", m.ctxSoftThreshold, m.ctxHardThreshold)
	}
	m.SetContextThresholds(0.5, 0.85)
	if m.ctxSoftThreshold != 0.5 || m.ctxHardThreshold != 0.85 {
		t.Fatalf("valid assign failed: %v/%v", m.ctxSoftThreshold, m.ctxHardThreshold)
	}
}

// TestTokenPctStringThresholdColouring checks the three regions: below
// soft → plain, between soft and hard → warning styled, at/above hard
// → error styled. We detect styling by looking for ANSI escape bytes.
func TestTokenPctStringThresholdColouring(t *testing.T) {
	lipgloss.SetColorProfile(0) // force styles to still emit escapes in tests
	// Note: lipgloss profile 0 = true-color; non-zero values would strip
	// escapes. We want them visible so we can grep.

	m := &Model{
		ctxSoftThreshold: 0.70,
		ctxHardThreshold: 0.90,
		provider:         fakeCappedProvider{max: 100},
	}

	check := func(input int, wantHasEsc bool, tag string) {
		t.Helper()
		m.usage = agent.Usage{InputTokens: input}
		got := tokenPctString(m)
		hasEsc := strings.Contains(got, "\x1b[")
		if hasEsc != wantHasEsc {
			t.Errorf("%s: pct=%q hasEsc=%v want %v", tag, got, hasEsc, wantHasEsc)
		}
	}
	// Below soft — no styling.
	check(50, false, "50%")
	// Between soft (70%) and hard (90%) — warning styled.
	check(80, true, "80%")
	// At/above hard — error styled.
	check(95, true, "95%")
}

// TestTokenPctStringZeroWhenNoUsage covers the pre-first-turn state.
func TestTokenPctStringZeroWhenNoUsage(t *testing.T) {
	m := &Model{
		ctxSoftThreshold: 0.70,
		ctxHardThreshold: 0.90,
		provider:         fakeCappedProvider{max: 100},
	}
	if got := tokenPctString(m); got != "0%" {
		t.Fatalf("got %q want 0%%", got)
	}
}

// TestRenderContextStatus_Regions exercises the three threshold regions
// plus the degraded "no token counter" path.
func TestRenderContextStatus_Regions(t *testing.T) {
	base := func() *Model {
		return &Model{
			ctxSoftThreshold:    0.70,
			ctxHardThreshold:    0.90,
			provider:            fakeCappedProvider{max: 100},
			tokenCounterChecked: true,
			tokenCounterPresent: true,
			providerName:        "test",
		}
	}

	// Healthy: 50% → "healthy" string.
	m := base()
	m.usage.InputTokens = 50
	got := m.renderContextStatus()
	if !strings.Contains(got, "healthy") {
		t.Errorf("healthy region: missing 'healthy' in %q", got)
	}
	if !strings.Contains(got, "50.0%") {
		t.Errorf("healthy region: pct not surfaced: %q", got)
	}

	// Soft: 80% → "above soft threshold" + fork recommendation.
	m = base()
	m.usage.InputTokens = 80
	got = m.renderContextStatus()
	if !strings.Contains(got, "above soft") {
		t.Errorf("soft region missing status: %q", got)
	}
	if !strings.Contains(got, "fork") {
		t.Errorf("soft region missing fork hint: %q", got)
	}

	// Hard: 95% → "above hard threshold" + /compact mention.
	m = base()
	m.usage.InputTokens = 95
	got = m.renderContextStatus()
	if !strings.Contains(got, "above hard") {
		t.Errorf("hard region missing status: %q", got)
	}
	if !strings.Contains(got, "/compact") {
		t.Errorf("hard region missing /compact hint: %q", got)
	}

	// No token counter: degraded path.
	m = base()
	m.tokenCounterPresent = false
	got = m.renderContextStatus()
	if !strings.Contains(got, "unavailable") {
		t.Errorf("degraded path missing 'unavailable': %q", got)
	}
}
