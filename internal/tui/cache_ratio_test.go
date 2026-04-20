package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

// TestCacheHitRatio_FormulaCorrect: ratio is cache / (cache + input).
// A 1500-token turn with 1000 served from cache is 40% cache-hit.
func TestCacheHitRatio_FormulaCorrect(t *testing.T) {
	cases := []struct {
		in    agent.Usage
		want  float64
		label string
	}{
		{agent.Usage{}, 0, "zero usage"},
		{agent.Usage{InputTokens: 1000}, 0, "no cache reads"},
		{agent.Usage{InputTokens: 1500, CacheReadTokens: 1000}, 0.4, "mixed"},
		{agent.Usage{InputTokens: 0, CacheReadTokens: 1000}, 1.0, "100% cache"},
	}
	for _, c := range cases {
		got := cacheHitRatio(c.in)
		if diff := got - c.want; diff < -0.001 || diff > 0.001 {
			t.Errorf("%s: got %f, want %f", c.label, got, c.want)
		}
	}
}

// TestStatusRow_RendersCacheRatio: when non-zero cache ratio, status
// strip includes a "cache NN%" segment. When zero, segment is absent.
func TestStatusRow_RendersCacheRatio(t *testing.T) {
	m := queueModel(t)
	m.usage.InputTokens = 600
	m.usage.CacheReadTokens = 400
	m.usage.CostUSD = 0.05

	got := m.renderStatus(120)
	if !strings.Contains(got, "cache") {
		t.Errorf("status should mention cache when ratio non-zero: %q", got)
	}

	m.usage.CacheReadTokens = 0
	got2 := m.renderStatus(120)
	if strings.Contains(got2, "cache") {
		t.Errorf("status should hide cache when ratio zero: %q", got2)
	}
}
