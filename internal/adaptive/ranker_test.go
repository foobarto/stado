package adaptive

import (
	"github.com/foobarto/stado/internal/artifacts"
	"testing"
	"time"
)

func TestRankIsShadowExplainableAndProtectsMandatory(t *testing.T) {
	now := time.Now()
	failed := artifacts.UsageObservation{Event: artifacts.UsageFailed, Evaluator: "test", CreatedAt: now}
	inputs := []Input{{Artifact: artifacts.Artifact{ID: "global", Version: 1, Scope: artifacts.ScopeGlobal}, LexicalScore: 1, Usage: []artifacts.UsageObservation{failed}}, {Artifact: artifacts.Artifact{ID: "session", Version: 2, Scope: artifacts.ScopeSession}, LexicalScore: 1, Usage: []artifacts.UsageObservation{failed}, Mandatory: true}}
	got := Rank(inputs)
	if len(got) != 2 || got[0].ArtifactID != "session" || !got[0].Shadow || got[0].PolicyVersion != PolicyVersion {
		t.Fatalf("got=%+v", got)
	}
	for _, r := range got {
		if r.ArtifactID == "session" && r.Score < 13 {
			t.Fatalf("mandatory demoted: %+v", r)
		}
	}
}
