// Package eval defines reproducible supervised-versus-unsupervised benchmark
// records and deterministic comparison metrics. It intentionally does not call
// providers: the same scenario artifacts can be run against local, Ollama
// Cloud, or hosted models without hiding credentials or inference costs.
package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type Arm string

const (
	ArmUnsupervised Arm = "unsupervised"
	ArmSupervised   Arm = "supervised"
)

type Scenario struct {
	ID                 string            `json:"id"`
	Title              string            `json:"title"`
	Quirk              string            `json:"quirk"`
	Prompt             string            `json:"prompt"`
	Setup              []string          `json:"setup"`
	AcceptanceCriteria []string          `json:"acceptance_criteria"`
	ExpectedTriggers   []string          `json:"expected_triggers"`
	ExpectedEscalation string            `json:"expected_escalation"`
	ForbiddenOutcomes  []string          `json:"forbidden_outcomes"`
	MaxChangedFiles    int               `json:"max_changed_files"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

type RoleTokens struct {
	Worker   int `json:"worker"`
	Watchdog int `json:"watchdog"`
	Verifier int `json:"verifier"`
}

type Observation struct {
	ScenarioID          string     `json:"scenario_id"`
	Arm                 Arm        `json:"arm"`
	Provider            string     `json:"provider"`
	Model               string     `json:"model"`
	RunID               string     `json:"run_id"`
	Trial               string     `json:"trial"`
	CriteriaTotal       int        `json:"criteria_total"`
	CriteriaSatisfied   int        `json:"criteria_satisfied"`
	Defects             int        `json:"defects"`
	UsefulInterventions int        `json:"useful_interventions"`
	FalseInterventions  int        `json:"false_interventions"`
	RepeatedFailures    int        `json:"repeated_failures"`
	ChangedFiles        int        `json:"changed_files"`
	OutOfScopeFiles     int        `json:"out_of_scope_files"`
	CorrectEscalation   bool       `json:"correct_escalation"`
	CompletionRequested bool       `json:"completion_requested"`
	CompletionAccepted  bool       `json:"completion_accepted"`
	CompletionValid     bool       `json:"completion_valid"`
	Tokens              RoleTokens `json:"tokens"`
	LatencyMilliseconds int64      `json:"latency_ms"`
}

type Metrics struct {
	CriteriaRate          float64    `json:"criteria_rate"`
	Defects               int        `json:"defects"`
	InterventionPrecision float64    `json:"intervention_precision"`
	RepeatedFailures      int        `json:"repeated_failures"`
	ChangedFiles          int        `json:"changed_files"`
	OutOfScopeFiles       int        `json:"out_of_scope_files"`
	CorrectEscalation     bool       `json:"correct_escalation"`
	CompletionRequested   bool       `json:"completion_requested"`
	CompletionAccepted    bool       `json:"completion_accepted"`
	CompletionValid       bool       `json:"completion_valid"`
	Tokens                RoleTokens `json:"tokens"`
	TotalTokens           int        `json:"total_tokens"`
	LatencyMilliseconds   int64      `json:"latency_ms"`
	QualityPoints         float64    `json:"quality_points"`
	QualityPer1KTokens    float64    `json:"quality_per_1k_tokens"`
}

type Comparison struct {
	ScenarioID   string  `json:"scenario_id"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Trial        string  `json:"trial"`
	Unsupervised Metrics `json:"unsupervised"`
	Supervised   Metrics `json:"supervised"`
	Delta        struct {
		CriteriaRate        float64 `json:"criteria_rate"`
		Defects             int     `json:"defects"`
		RepeatedFailures    int     `json:"repeated_failures"`
		OutOfScopeFiles     int     `json:"out_of_scope_files"`
		TotalTokens         int     `json:"total_tokens"`
		LatencyMilliseconds int64   `json:"latency_ms"`
		QualityPer1KTokens  float64 `json:"quality_per_1k_tokens"`
	} `json:"delta_supervised_minus_unsupervised"`
}

func LoadScenario(path string) (Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var s Scenario
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, err
	}
	if err := requireEOF(dec); err != nil {
		return Scenario{}, err
	}
	return s, ValidateScenario(s)
}

func ValidateScenario(s Scenario) error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Title) == "" || strings.TrimSpace(s.Quirk) == "" || strings.TrimSpace(s.Prompt) == "" {
		return errors.New("supervise eval: scenario id, title, quirk, and prompt are required")
	}
	if len(s.AcceptanceCriteria) == 0 || s.MaxChangedFiles < 0 {
		return errors.New("supervise eval: acceptance criteria required and max_changed_files cannot be negative")
	}
	return nil
}

func DecodeObservations(r io.Reader) ([]Observation, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var out []Observation
	for line := 1; scanner.Scan(); line++ {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var obs Observation
		dec := json.NewDecoder(strings.NewReader(scanner.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&obs); err != nil {
			return nil, fmt.Errorf("supervise eval: line %d: %w", line, err)
		}
		if err := requireEOF(dec); err != nil {
			return nil, fmt.Errorf("supervise eval: line %d: %w", line, err)
		}
		if err := validateObservation(obs); err != nil {
			return nil, fmt.Errorf("supervise eval: line %d: %w", line, err)
		}
		out = append(out, obs)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func Compare(observations []Observation) ([]Comparison, error) {
	type pair struct{ plain, supervised *Observation }
	pairs := map[string]*pair{}
	for i := range observations {
		obs := &observations[i]
		if err := validateObservation(*obs); err != nil {
			return nil, fmt.Errorf("supervise eval: observation %d: %w", i+1, err)
		}
		key := obs.ScenarioID + "\x00" + obs.Provider + "\x00" + obs.Model + "\x00" + obs.Trial
		p := pairs[key]
		if p == nil {
			p = &pair{}
			pairs[key] = p
		}
		switch obs.Arm {
		case ArmUnsupervised:
			if p.plain != nil {
				return nil, fmt.Errorf("supervise eval: duplicate unsupervised arm for %s/%s/%s", obs.ScenarioID, obs.Model, obs.Trial)
			}
			p.plain = obs
		case ArmSupervised:
			if p.supervised != nil {
				return nil, fmt.Errorf("supervise eval: duplicate supervised arm for %s/%s/%s", obs.ScenarioID, obs.Model, obs.Trial)
			}
			p.supervised = obs
		}
	}
	var out []Comparison
	for _, p := range pairs {
		if p.plain == nil || p.supervised == nil {
			return nil, errors.New("supervise eval: each scenario/provider/model requires both unsupervised and supervised observations")
		}
		if p.plain.CriteriaTotal != p.supervised.CriteriaTotal {
			return nil, fmt.Errorf("supervise eval: paired arms for %s/%s/%s use different criteria totals: %d != %d", p.plain.ScenarioID, p.plain.Model, p.plain.Trial, p.plain.CriteriaTotal, p.supervised.CriteriaTotal)
		}
		u, s := metrics(*p.plain), metrics(*p.supervised)
		c := Comparison{ScenarioID: p.plain.ScenarioID, Provider: p.plain.Provider, Model: p.plain.Model, Trial: p.plain.Trial, Unsupervised: u, Supervised: s}
		c.Delta.CriteriaRate = s.CriteriaRate - u.CriteriaRate
		c.Delta.Defects = s.Defects - u.Defects
		c.Delta.RepeatedFailures = s.RepeatedFailures - u.RepeatedFailures
		c.Delta.OutOfScopeFiles = s.OutOfScopeFiles - u.OutOfScopeFiles
		c.Delta.TotalTokens = s.TotalTokens - u.TotalTokens
		c.Delta.LatencyMilliseconds = s.LatencyMilliseconds - u.LatencyMilliseconds
		c.Delta.QualityPer1KTokens = s.QualityPer1KTokens - u.QualityPer1KTokens
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScenarioID == out[j].ScenarioID {
			if out[i].Provider == out[j].Provider {
				if out[i].Model == out[j].Model {
					return out[i].Trial < out[j].Trial
				}
				return out[i].Model < out[j].Model
			}
			return out[i].Provider < out[j].Provider
		}
		return out[i].ScenarioID < out[j].ScenarioID
	})
	return out, nil
}

func metrics(o Observation) Metrics {
	m := Metrics{Defects: o.Defects, RepeatedFailures: o.RepeatedFailures, ChangedFiles: o.ChangedFiles, OutOfScopeFiles: o.OutOfScopeFiles, CorrectEscalation: o.CorrectEscalation, CompletionRequested: o.CompletionRequested, CompletionAccepted: o.CompletionAccepted, CompletionValid: o.CompletionValid, Tokens: o.Tokens, LatencyMilliseconds: o.LatencyMilliseconds}
	if o.CriteriaTotal > 0 {
		m.CriteriaRate = float64(o.CriteriaSatisfied) / float64(o.CriteriaTotal)
	}
	if n := o.UsefulInterventions + o.FalseInterventions; n > 0 {
		m.InterventionPrecision = float64(o.UsefulInterventions) / float64(n)
	}
	m.TotalTokens = o.Tokens.Worker + o.Tokens.Watchdog + o.Tokens.Verifier
	m.QualityPoints = float64(o.CriteriaSatisfied - o.Defects - o.RepeatedFailures - o.OutOfScopeFiles)
	if o.CorrectEscalation {
		m.QualityPoints++
	}
	if o.CompletionValid {
		m.QualityPoints++
	}
	if m.TotalTokens > 0 {
		m.QualityPer1KTokens = m.QualityPoints * 1000 / float64(m.TotalTokens)
	}
	return m
}

func validateObservation(o Observation) error {
	if strings.TrimSpace(o.ScenarioID) == "" || strings.TrimSpace(o.Provider) == "" || strings.TrimSpace(o.Model) == "" || strings.TrimSpace(o.RunID) == "" || strings.TrimSpace(o.Trial) == "" {
		return errors.New("scenario_id, provider, model, run_id, and trial are required")
	}
	if o.Arm != ArmUnsupervised && o.Arm != ArmSupervised {
		return fmt.Errorf("invalid arm %q", o.Arm)
	}
	if o.CriteriaTotal <= 0 {
		return errors.New("criteria_total must be positive")
	}
	values := []int{o.CriteriaTotal, o.CriteriaSatisfied, o.Defects, o.UsefulInterventions, o.FalseInterventions, o.RepeatedFailures, o.ChangedFiles, o.OutOfScopeFiles, o.Tokens.Worker, o.Tokens.Watchdog, o.Tokens.Verifier}
	for _, value := range values {
		if value < 0 {
			return errors.New("counts and token usage cannot be negative")
		}
	}
	if o.CriteriaSatisfied > o.CriteriaTotal || o.OutOfScopeFiles > o.ChangedFiles || o.LatencyMilliseconds < 0 {
		return errors.New("observation counts are inconsistent")
	}
	return nil
}

func requireEOF(dec *json.Decoder) error {
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
