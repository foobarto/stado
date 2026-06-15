package tui

import "testing"

func TestTotalTokens_AccumulatesInputAcrossTurns(t *testing.T) {
	m := &Model{}
	m.cumulativeInputTokens = 0
	m.usage.OutputTokens = 500

	// Turn 1: last-turn input is 1000, cumulative should be 1000.
	m.usage.InputTokens = 1000
	m.cumulativeInputTokens += 1000
	if got := m.totalTokens(); got != 1500 {
		t.Fatalf("after turn 1 totalTokens=%d, want 1500", got)
	}

	// Turn 2: last-turn input drops to 200 (context trim), but cumulative grows.
	m.usage.InputTokens = 200
	m.cumulativeInputTokens += 200
	m.usage.OutputTokens += 300
	if got := m.totalTokens(); got != 2000 {
		t.Fatalf("after turn 2 totalTokens=%d, want 2000 (1200 cumulative in + 800 out)", got)
	}
}
