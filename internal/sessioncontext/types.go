package sessioncontext

import "time"

type State struct {
	SessionID      string            `json:"session_id"`
	Version        uint64            `json:"version"`
	Objective      string            `json:"objective,omitempty"`
	CurrentTask    string            `json:"current_task,omitempty"`
	Blockers       []string          `json:"blockers,omitempty"`
	NextStep       string            `json:"next_step,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	ActiveChildren []string          `json:"active_children,omitempty"`
	Verification   string            `json:"verification,omitempty"`
	TokenUsage     uint64            `json:"token_usage,omitempty"`
	CostMicros     uint64            `json:"cost_micros,omitempty"`
	Assertions     map[string]string `json:"assertions,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type StatePatch struct {
	CurrentTask string   `json:"current_task,omitempty"`
	Blockers    []string `json:"blockers,omitempty"`
	NextStep    string   `json:"next_step,omitempty"`
}

type HostPatch struct {
	Objective      *string
	Capabilities   []string
	ActiveChildren []string
	Verification   *string
	TokenUsage     *uint64
	CostMicros     *uint64
}

type DecisionStatus string

const (
	DecisionProposed   DecisionStatus = "proposed"
	DecisionAccepted   DecisionStatus = "accepted"
	DecisionSuperseded DecisionStatus = "superseded"
	DecisionRejected   DecisionStatus = "rejected"
)

type Decision struct {
	ID           string         `json:"id"`
	SessionID    string         `json:"session_id"`
	Version      uint64         `json:"version"`
	Status       DecisionStatus `json:"status"`
	Context      string         `json:"context"`
	Choice       string         `json:"decision"`
	Alternatives []string       `json:"alternatives,omitempty"`
	Consequences []string       `json:"consequences,omitempty"`
	EvidenceRefs []string       `json:"evidence_refs,omitempty"`
	CreatedBy    string         `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type SignalType string

const (
	SignalRepeatedToolFailure    SignalType = "repeated_tool_failure"
	SignalArgumentChangedSuccess SignalType = "argument_changed_then_success"
	SignalVerificationRecovered  SignalType = "verification_fail_then_pass"
	SignalRecurringDenial        SignalType = "recurring_permission_or_scope_denial"
	SignalOperatorCorrection     SignalType = "explicit_operator_correction"
)

type Signal struct {
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	Type            SignalType        `json:"type"`
	DetectorVersion int               `json:"detector_version"`
	Confidence      string            `json:"confidence"`
	OriginRefs      []string          `json:"origin_refs"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
}

type ObservationKind string

const (
	ObservationTool         ObservationKind = "tool_outcome"
	ObservationVerification ObservationKind = "verification"
	ObservationDenial       ObservationKind = "permission_or_scope_denial"
	ObservationCorrection   ObservationKind = "operator_correction"
)

type Observation struct {
	ID          string            `json:"id"`
	SessionID   string            `json:"session_id"`
	Kind        ObservationKind   `json:"kind"`
	Tool        string            `json:"tool,omitempty"`
	ArgsDigest  string            `json:"args_digest,omitempty"`
	Succeeded   bool              `json:"succeeded,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	EvidenceRef string            `json:"evidence_ref"`
	CreatedAt   time.Time         `json:"created_at"`
}

type JournalEntry struct {
	Sequence uint64    `json:"sequence"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Actor    string    `json:"actor"`
	Summary  string    `json:"summary,omitempty"`
	Ref      string    `json:"ref,omitempty"`
}
