package main

import (
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// B3: real trace commits carry zero tokens, yet the work is real. usage must
// count distinct turns (RecordedTurns) so it can report activity honestly
// instead of the misleading "No turns recorded".
func TestUsage_CountsTurnsWithoutTokenData(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "usage-b3", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Two distinct turns of tool activity, all with zero tokens (the real
	// shape produced by the agent loop today).
	for _, meta := range []stadogit.CommitMeta{
		{Tool: "bash", Model: "claude", Turn: 1},
		{Tool: "read", Model: "claude", Turn: 1},
		{Tool: "write", Model: "claude", Turn: 2},
	} {
		if _, err := sess.CommitToTrace(meta); err != nil {
			t.Fatal(err)
		}
	}

	modelAgg := map[string]*usageModelStats{}
	ss, err := walkSessionTrace(sc, sess.ID, time.Now().Add(-time.Hour), time.Time{}, modelAgg)
	if err != nil {
		t.Fatal(err)
	}

	if ss.RecordedTurns != 2 {
		t.Errorf("RecordedTurns = %d, want 2 distinct turns", ss.RecordedTurns)
	}
	// No fabricated token data: Turns + model aggregation stay empty.
	if ss.Turns != 0 {
		t.Errorf("Turns = %d, want 0 (no token data recorded)", ss.Turns)
	}
	if len(modelAgg) != 0 {
		t.Errorf("modelAgg should be empty without token data, got %d entries", len(modelAgg))
	}
}

// Indented body lines (e.g. a compaction summary) must not parse as trailers
// — the Codex #143 injection vector that would otherwise inflate turn counts.
func TestParseTrailers_IgnoresIndentedBodyLines(t *testing.T) {
	msg := "bash(x): did a thing\n\n  Turn: 5\n  Tool: fake\nTurn: 2\nModel: claude\n"
	tr := parseTrailers(msg)
	if tr["turn"] != "2" {
		t.Errorf("turn = %q, want 2 (indented 'Turn: 5' must be ignored)", tr["turn"])
	}
	if tr["tool"] != "" {
		t.Errorf("indented 'Tool: fake' must not parse as a trailer, got %q", tr["tool"])
	}
}
