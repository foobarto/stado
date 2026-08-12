package adaptive

import (
	"encoding/json"
	"sort"

	"github.com/foobarto/stado/internal/broker/wal"
)

type Report struct {
	PolicyVersion      string   `json:"policy_version"`
	Evaluations        int      `json:"evaluations"`
	CandidateScores    int      `json:"candidate_scores"`
	Surfaced           int      `json:"surfaced"`
	ShadowTopDifferent int      `json:"shadow_top_different"`
	Sessions           []string `json:"sessions"`
}

func BuildReport(records []wal.Record) (Report, error) {
	r := Report{PolicyVersion: PolicyVersion}
	sessions := map[string]bool{}
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != "adaptive_retrieval" || event.Type != "shadow.evaluated" {
				continue
			}
			var e Evaluation
			if err := json.Unmarshal(event.Data, &e); err != nil {
				return Report{}, err
			}
			r.Evaluations++
			r.CandidateScores += len(e.Scores)
			r.Surfaced += len(e.ActuallySurfaced)
			sessions[e.SessionID] = true
			if len(e.Scores) > 0 && len(e.ActuallySurfaced) > 0 && e.Scores[0].ArtifactID != e.ActuallySurfaced[0] {
				r.ShadowTopDifferent++
			}
		}
	}
	for session := range sessions {
		r.Sessions = append(r.Sessions, session)
	}
	sort.Strings(r.Sessions)
	return r, nil
}
