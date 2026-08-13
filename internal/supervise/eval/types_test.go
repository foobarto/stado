package eval

import (
	"strings"
	"testing"
)

func TestCompareReportsQualityAndRoleTokenTradeoff(t *testing.T) {
	rows := `{"scenario_id":"thrash","arm":"unsupervised","provider":"ollama-cloud","model":"small","run_id":"u","trial":"seed-1","criteria_total":4,"criteria_satisfied":2,"defects":1,"repeated_failures":3,"changed_files":5,"out_of_scope_files":2,"correct_escalation":false,"completion_requested":true,"completion_accepted":true,"completion_valid":false,"tokens":{"worker":1000,"watchdog":0,"verifier":0},"latency_ms":1000}
{"scenario_id":"thrash","arm":"supervised","provider":"ollama-cloud","model":"small","run_id":"s","trial":"seed-1","criteria_total":4,"criteria_satisfied":4,"defects":0,"useful_interventions":2,"false_interventions":0,"repeated_failures":1,"changed_files":3,"out_of_scope_files":0,"correct_escalation":true,"completion_requested":true,"completion_accepted":true,"completion_valid":true,"tokens":{"worker":1100,"watchdog":300,"verifier":200},"latency_ms":1800}`
	observations, err := DecodeObservations(strings.NewReader(rows))
	if err != nil {
		t.Fatal(err)
	}
	comparisons, err := Compare(observations)
	if err != nil {
		t.Fatal(err)
	}
	got := comparisons[0]
	if got.Delta.CriteriaRate != .5 || got.Delta.RepeatedFailures != -2 || got.Supervised.Tokens.Watchdog != 300 || got.Supervised.InterventionPrecision != 1 || got.Delta.QualityPer1KTokens <= 0 {
		t.Fatalf("comparison = %+v", got)
	}
}

func TestCompareRequiresPairedArms(t *testing.T) {
	_, err := Compare([]Observation{{ScenarioID: "x", Arm: ArmSupervised, Provider: "p", Model: "m", RunID: "1", Trial: "seed-1"}})
	if err == nil {
		t.Fatal("unpaired observation accepted")
	}
}

func TestCompareRejectsMismatchedCriteriaTotals(t *testing.T) {
	observations := []Observation{
		{ScenarioID: "x", Arm: ArmUnsupervised, Provider: "p", Model: "m", RunID: "u", Trial: "seed-1", CriteriaTotal: 2},
		{ScenarioID: "x", Arm: ArmSupervised, Provider: "p", Model: "m", RunID: "s", Trial: "seed-1", CriteriaTotal: 3},
	}
	_, err := Compare(observations)
	if err == nil {
		t.Fatal("mismatched criteria totals accepted")
	}
	for _, want := range []string{"different criteria totals", "2 != 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}
