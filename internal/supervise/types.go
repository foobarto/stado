// Package supervise implements the host-owned contract and enforcement state
// specified by EP-0062 (docs/eps/0062-harness-enforced-supervised-work.md).
// Model output may propose changes and verdicts, but only the service transition
// rules can grant authority or complete a run.
package supervise

import "time"

type Mode string

const (
	ModeEvent Mode = "event"
	ModeLive  Mode = "live"
)

type PivotApproval string

const (
	PivotByUser     PivotApproval = "user"
	PivotByWatchdog PivotApproval = "watchdog"
)

type Profile string

const (
	ProfileStandard      Profile = "standard"
	ProfileHighAssurance Profile = "high_assurance"
	ProfileCustom        Profile = "custom"
)

type Thinking string

const (
	ThinkingAuto Thinking = "auto"
	ThinkingOn   Thinking = "on"
	ThinkingOff  Thinking = "off"
)

type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

type RoleProfile struct {
	Provider             string   `json:"provider,omitempty"`
	Model                string   `json:"model,omitempty"`
	Thinking             Thinking `json:"thinking"`
	ThinkingBudgetTokens int      `json:"thinking_budget_tokens,omitempty"`
	Effort               Effort   `json:"effort"`
}

type RoleBudget struct {
	// Reviewer spend limits are intentionally token-only in EP-0062 v1. Do not
	// infer a monetary authority boundary from provider-reported cost metadata.
	TokenCap       int `json:"token_cap,omitempty"`
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

type Config struct {
	Mode                  Mode          `json:"mode"`
	PivotApproval         PivotApproval `json:"pivot_approval"`
	Profile               Profile       `json:"profile"`
	Watchdog              RoleProfile   `json:"watchdog"`
	Verifier              RoleProfile   `json:"verifier"`
	WatchdogBudget        RoleBudget    `json:"watchdog_budget"`
	VerifierBudget        RoleBudget    `json:"verifier_budget"`
	EventReviewRetries    int           `json:"event_review_retries"`
	FailedEventLimit      int           `json:"failed_event_limit"`
	CorrectionLimit       int           `json:"correction_limit"`
	LiveRetryBaseMillis   int           `json:"live_retry_base_millis"`
	LiveRetryMaxMillis    int           `json:"live_retry_max_millis"`
	WatchdogRequired      bool          `json:"watchdog_required"`
	VerifierRequired      bool          `json:"verifier_required"`
	AllowAdvisoryFallback bool          `json:"allow_advisory_fallback"`
}

type Step struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	DoneWhen string `json:"done_when"`
}

type Baseline struct {
	Objective          string   `json:"objective"`
	Constraints        []string `json:"constraints,omitempty"`
	NonGoals           []string `json:"non_goals,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Plan               []Step   `json:"plan"`
	DefinitionOfDone   []string `json:"definition_of_done"`
	Verification       []string `json:"verification"`
	Risks              []string `json:"risks,omitempty"`
}

type Status string

const (
	StatusSetup            Status = "setup"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusRunning          Status = "running"
	StatusPivotPending     Status = "pivot_pending"
	StatusVerifying        Status = "verifying"
	StatusPaused           Status = "paused"
	StatusCompleted        Status = "completed"
	StatusCancelled        Status = "cancelled"
)

type ActorRole string

const (
	RoleHost     ActorRole = "host"
	RoleOperator ActorRole = "operator"
	RoleWorker   ActorRole = "worker"
	RoleWatchdog ActorRole = "watchdog"
	RoleVerifier ActorRole = "verifier"
)

// Anchor binds a model judgment to the exact worker and plan state it saw.
// A verdict with an older session sequence, plan version, active step, or tree
// digest cannot authorize a transition after the worker has moved on.
type Anchor struct {
	RootSessionID   string `json:"root_session_id"`
	SessionSequence uint64 `json:"session_sequence"`
	PlanVersion     uint64 `json:"plan_version"`
	ActiveStep      string `json:"active_step,omitempty"`
	TreeDigest      string `json:"tree_digest"`
}

type Evidence struct {
	Kind       string    `json:"kind"`
	Summary    string    `json:"summary"`
	References []string  `json:"references,omitempty"`
	Anchor     Anchor    `json:"anchor"`
	CreatedAt  time.Time `json:"created_at"`
}

type PivotKind string

const (
	PivotTactical PivotKind = "tactical"
	PivotPlan     PivotKind = "plan"
	PivotContract PivotKind = "contract"
)

type PivotRequest struct {
	ID               string    `json:"id"`
	Kind             PivotKind `json:"kind"`
	Reason           string    `json:"reason"`
	ProposedBaseline *Baseline `json:"proposed_baseline,omitempty"`
	ProposedPlan     []Step    `json:"proposed_plan,omitempty"`
	Anchor           Anchor    `json:"anchor"`
	RequestedAt      time.Time `json:"requested_at"`
}

type CompletionRequest struct {
	Summary     string     `json:"summary"`
	Evidence    []Evidence `json:"evidence"`
	Anchor      Anchor     `json:"anchor"`
	RequestedAt time.Time  `json:"requested_at"`
}

// Followup is operator input captured while the worker is occupied. The host
// durably owns this inbox until a fresh watchdog classifies the input and the
// host either delivers it to the worker or creates a backlog task from it.
type Followup struct {
	ID          string    `json:"id"`
	Text        string    `json:"text"`
	RequestedAt time.Time `json:"requested_at"`
}

type ReviewKind string

const (
	ReviewBaseline   ReviewKind = "baseline"
	ReviewEvent      ReviewKind = "event"
	ReviewPivot      ReviewKind = "pivot"
	ReviewCompletion ReviewKind = "completion"
	ReviewFollowup   ReviewKind = "followup"
)

type VerdictDecision string

const (
	VerdictApprove  VerdictDecision = "approve"
	VerdictReject   VerdictDecision = "reject"
	VerdictCorrect  VerdictDecision = "correct"
	VerdictPause    VerdictDecision = "pause"
	VerdictStop     VerdictDecision = "stop"
	VerdictContinue VerdictDecision = "continue"
)

type Handoff struct {
	OpenConcerns    []string `json:"open_concerns,omitempty"`
	Hypotheses      []string `json:"hypotheses,omitempty"`
	Interventions   []string `json:"interventions,omitempty"`
	MissingEvidence []string `json:"missing_evidence,omitempty"`
	SuggestedProbes []string `json:"suggested_probes,omitempty"`
}

type Verdict struct {
	Kind         ReviewKind      `json:"kind"`
	Decision     VerdictDecision `json:"decision"`
	Anchor       Anchor          `json:"anchor"`
	Rationale    string          `json:"rationale"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Correction   string          `json:"correction,omitempty"`
	Handoff      Handoff         `json:"handoff,omitempty"`
	ReviewedAt   time.Time       `json:"reviewed_at"`
}

// StepApproval is host-derived authority to advance one exact active step.
// The watchdog cannot manufacture it in verdict JSON: the service records it
// only when an event review covered a current step-completion trigger and the
// verdict cited that trigger's served worker-event evidence.
type StepApproval struct {
	Anchor      Anchor `json:"anchor"`
	TriggerID   string `json:"trigger_id"`
	EvidenceRef string `json:"evidence_ref"`
}

type State struct {
	ID            string `json:"id"`
	Version       uint64 `json:"version"`
	Status        Status `json:"status"`
	RootSessionID string `json:"root_session_id"`
	// AttachedSessionID is the current worker-session continuation for this
	// immutable root. It changes only when the host adopts an automatic
	// context-recovery fork; older records without it are attached to the root.
	AttachedSessionID     string             `json:"attached_session_id,omitempty"`
	ObjectiveSeed         string             `json:"objective_seed,omitempty"`
	Config                Config             `json:"config"`
	Baseline              Baseline           `json:"baseline"`
	ContractDigest        string             `json:"contract_digest,omitempty"`
	PlanVersion           uint64             `json:"plan_version"`
	ActiveStep            string             `json:"active_step,omitempty"`
	CompletedSteps        []string           `json:"completed_steps,omitempty"`
	SessionSequence       uint64             `json:"session_sequence"`
	InitialTreeDigest     string             `json:"initial_tree_digest,omitempty"`
	TreeDigest            string             `json:"tree_digest,omitempty"`
	Evidence              []Evidence         `json:"evidence,omitempty"`
	PendingPivot          *PivotRequest      `json:"pending_pivot,omitempty"`
	Completion            *CompletionRequest `json:"completion,omitempty"`
	PendingFollowups      []Followup         `json:"pending_followups,omitempty"`
	LastVerdict           *Verdict           `json:"last_verdict,omitempty"`
	StepApproval          *StepApproval      `json:"step_approval,omitempty"`
	WatchdogHandoff       Handoff            `json:"watchdog_handoff,omitempty"`
	Detector              DetectorSnapshot   `json:"detector"`
	FailedEventStreak     int                `json:"failed_event_streak"`
	FailedCorrectionCount int                `json:"failed_correction_count"`
	PauseReason           string             `json:"pause_reason,omitempty"`
	ResumeStatus          Status             `json:"resume_status,omitempty"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

func (s State) Anchor() Anchor {
	return Anchor{
		RootSessionID: s.RootSessionID, SessionSequence: s.SessionSequence,
		PlanVersion: s.PlanVersion, ActiveStep: s.ActiveStep, TreeDigest: s.TreeDigest,
	}
}
