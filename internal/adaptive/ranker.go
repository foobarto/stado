// Package adaptive computes explainable retrieval scores. The first release is
// shadow-only: it cannot change prompt eligibility or artifact authority.
package adaptive

import (
	"github.com/foobarto/stado/internal/artifacts"
	"sort"
	"strings"
	"time"
)

const PolicyVersion = "adaptive-shadow-v1"

type Input struct {
	Artifact      artifacts.Artifact
	Usage         []artifacts.UsageObservation
	LexicalScore  float64
	ScopeDistance int
	Now           time.Time
	Mandatory     bool
	Pinned        bool
}
type Score struct {
	ArtifactID           string    `json:"artifact_id"`
	ArtifactVersion      uint64    `json:"artifact_version"`
	Score                float64   `json:"score"`
	Reasons              []string  `json:"reasons"`
	PolicyVersion        string    `json:"policy_version"`
	ObservationWatermark time.Time `json:"observation_watermark"`
	Shadow               bool      `json:"shadow"`
}

func Rank(inputs []Input) []Score {
	out := make([]Score, 0, len(inputs))
	for _, in := range inputs {
		s := Score{ArtifactID: in.Artifact.ID, ArtifactVersion: in.Artifact.Version, Score: in.LexicalScore * 10, PolicyVersion: PolicyVersion, Shadow: true}
		s.Reasons = append(s.Reasons, "lexical")
		switch in.Artifact.Scope {
		case artifacts.ScopeSession:
			s.Score += 3
			s.Reasons = append(s.Reasons, "session-scope")
		case artifacts.ScopeRepo:
			s.Score += 2
			s.Reasons = append(s.Reasons, "repo-scope")
		case artifacts.ScopeGlobal:
			s.Score += 1
		}
		var helped, failed, contradicted int
		for _, o := range in.Usage {
			if o.CreatedAt.After(s.ObservationWatermark) {
				s.ObservationWatermark = o.CreatedAt
			}
			switch o.Event {
			case artifacts.UsageHelped:
				helped++
			case artifacts.UsageFailed:
				failed++
			case artifacts.UsageContradicted:
				contradicted++
			}
		}
		if helped > 0 {
			s.Score += float64(min(helped, 3))
			s.Reasons = append(s.Reasons, "externally-helped")
		}
		if failed > 0 && !in.Mandatory && !in.Pinned {
			s.Score -= float64(min(failed, 3))
			s.Reasons = append(s.Reasons, "externally-failed-shadow")
		}
		if contradicted > 0 {
			s.Reasons = append(s.Reasons, "contradiction-present")
		}
		if !in.Artifact.ExpiresAt.IsZero() && in.Artifact.ExpiresAt.Before(in.Now) {
			s.Score = -1e9
			s.Reasons = append(s.Reasons, "expired")
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return strings.Compare(out[i].ArtifactID, out[j].ArtifactID) < 0
	})
	return out
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
