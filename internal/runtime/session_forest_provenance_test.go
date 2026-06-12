package runtime

import (
	"testing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// TestBuildForest_StampsProvenanceCounts: a session whose trace ref carries
// hook-mutation provenance commits (Mutated-By-Hook / Deny-Reason, tagged
// Turn: N) surfaces those as per-turn TurnNode counts AND session-level
// SessionNode totals — the data behind the /tree `⟳N` / `⊘N` badge (spec
// hooks-audit-mutation-provenance STAGE 7b).
func TestBuildForest_StampsProvenanceCounts(t *testing.T) {
	cfg, sc, _ := forestEnv(t)
	// Two turns → turns/1 + turns/2 tags.
	sess := seedSession(t, cfg, sc, "prov", 2)

	// Turn 1: two mutations + one deny. Turn 2: one mutation. The trace
	// commits are written directly (the executor's two-commit model writes
	// the MUTATION commit carrying Mutated-By-Hook; only that half is
	// counted, matching production).
	commits := []stadogit.CommitMeta{
		{Tool: "bash", Summary: "exec [ok]", Turn: 1, ResultSHA: "m1", OriginalResultSHA: "o1", MutatedByHook: "redact"},
		{Tool: "bash", Summary: "exec [ok]", Turn: 1, ResultSHA: "m2", OriginalResultSHA: "o2", MutatedByHook: "redact"},
		{Tool: "bash", Summary: "exec [denied]", Turn: 1, Error: "denied", DenyReason: "blocked", DeniedByHook: "guard"},
		// A plain (non-provenance) trace commit must NOT be counted.
		{Tool: "read", Summary: "file [ok]", Turn: 1, ResultSHA: "plain"},
		{Tool: "bash", Summary: "exec [ok]", Turn: 2, ResultSHA: "m3", OriginalResultSHA: "o3", MutatedByHook: "redact"},
	}
	for _, m := range commits {
		if _, err := sess.CommitToTrace(m); err != nil {
			t.Fatalf("CommitToTrace: %v", err)
		}
	}

	f, err := BuildForest(sc, cfg.WorktreeDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	node := f.Sessions["prov"]
	if node == nil {
		t.Fatal("session 'prov' missing from forest")
	}

	// Session totals: 3 mutations (2 turn-1 + 1 turn-2), 1 deny.
	if node.MutatedTotal != 3 {
		t.Errorf("MutatedTotal = %d, want 3", node.MutatedTotal)
	}
	if node.DeniedTotal != 1 {
		t.Errorf("DeniedTotal = %d, want 1", node.DeniedTotal)
	}

	// Per-turn counts.
	byTurn := map[int]*TurnNode{}
	for _, tn := range node.Turns {
		byTurn[tn.Entry.Turn] = tn
	}
	if t1 := byTurn[1]; t1 == nil {
		t.Fatal("turn 1 node missing")
	} else {
		if t1.MutatedCount != 2 {
			t.Errorf("turn 1 MutatedCount = %d, want 2", t1.MutatedCount)
		}
		if t1.DeniedCount != 1 {
			t.Errorf("turn 1 DeniedCount = %d, want 1", t1.DeniedCount)
		}
	}
	if t2 := byTurn[2]; t2 == nil {
		t.Fatal("turn 2 node missing")
	} else {
		if t2.MutatedCount != 1 {
			t.Errorf("turn 2 MutatedCount = %d, want 1", t2.MutatedCount)
		}
		if t2.DeniedCount != 0 {
			t.Errorf("turn 2 DeniedCount = %d, want 0", t2.DeniedCount)
		}
	}
}

// TestBuildForest_NoProvenanceNoCounts: a session whose trace ref carries
// only plain tool-call commits leaves every count at zero (no badge). Guards
// against false positives — the walk must distinguish provenance commits
// from ordinary results.
func TestBuildForest_NoProvenanceNoCounts(t *testing.T) {
	cfg, sc, _ := forestEnv(t)
	sess := seedSession(t, cfg, sc, "plain", 1)
	for i := 0; i < 3; i++ {
		if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "read", Summary: "ok", Turn: 1, ResultSHA: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := BuildForest(sc, cfg.WorktreeDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	node := f.Sessions["plain"]
	if node == nil {
		t.Fatal("session 'plain' missing")
	}
	if node.MutatedTotal != 0 || node.DeniedTotal != 0 {
		t.Errorf("plain session should have zero provenance: mutated=%d denied=%d",
			node.MutatedTotal, node.DeniedTotal)
	}
	for _, tn := range node.Turns {
		if tn.MutatedCount != 0 || tn.DeniedCount != 0 {
			t.Errorf("turn %d should have zero counts: mutated=%d denied=%d",
				tn.Entry.Turn, tn.MutatedCount, tn.DeniedCount)
		}
	}
}
