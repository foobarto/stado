package supervise

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/foobarto/stado/internal/broker/wal"
)

const storeName = "supervise"

const maxStateBytes = 4 << 20

var (
	ErrNotFound     = errors.New("supervise: run not found")
	ErrVersion      = errors.New("supervise: version conflict")
	ErrInvalidState = errors.New("supervise: invalid state transition")
	ErrUnauthorized = errors.New("supervise: actor is not authorized")
	ErrStaleVerdict = errors.New("supervise: verdict anchor is stale")

	// The broker WAL is the single writer across processes, while callers can
	// still construct more than one Service over the same shared in-process
	// store. Serialize logical CAS folds across those instances so two version
	// N mutations cannot both append version N+1 and poison replay.
	serviceMutationMu sync.Mutex
)

type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}

type Service struct {
	mu  sync.Mutex
	wal appender
	now func() time.Time
}

func New(store appender) *Service { return &Service{wal: store, now: time.Now} }

func DefaultConfig() Config {
	return Config{
		Mode: ModeEvent, PivotApproval: PivotByUser, Profile: ProfileStandard,
		Watchdog:           RoleProfile{Thinking: ThinkingAuto, Effort: EffortHigh},
		Verifier:           RoleProfile{Thinking: ThinkingAuto, Effort: EffortHigh},
		WatchdogBudget:     RoleBudget{TokenCap: 48_000, TimeoutSeconds: 120},
		VerifierBudget:     RoleBudget{TokenCap: 64_000, TimeoutSeconds: 180},
		EventReviewRetries: 3, FailedEventLimit: 10, CorrectionLimit: 3,
		LiveRetryBaseMillis: 500, LiveRetryMaxMillis: 8_000,
		WatchdogRequired: false, VerifierRequired: true, AllowAdvisoryFallback: true,
	}
}

func NormalizeConfig(cfg Config) (Config, error) {
	d := DefaultConfig()
	if cfg == (Config{}) {
		cfg = d
	}
	if cfg.Mode == "" {
		cfg.Mode = d.Mode
	}
	if cfg.PivotApproval == "" {
		cfg.PivotApproval = d.PivotApproval
	}
	if cfg.Profile == "" {
		cfg.Profile = d.Profile
	}
	if cfg.Watchdog.Thinking == "" {
		cfg.Watchdog.Thinking = d.Watchdog.Thinking
	}
	if cfg.Watchdog.Effort == "" {
		cfg.Watchdog.Effort = d.Watchdog.Effort
	}
	if cfg.Verifier.Thinking == "" {
		cfg.Verifier.Thinking = d.Verifier.Thinking
	}
	if cfg.Verifier.Effort == "" {
		cfg.Verifier.Effort = d.Verifier.Effort
	}
	if cfg.EventReviewRetries == 0 {
		cfg.EventReviewRetries = d.EventReviewRetries
	}
	if cfg.FailedEventLimit == 0 {
		cfg.FailedEventLimit = d.FailedEventLimit
	}
	if cfg.CorrectionLimit == 0 {
		cfg.CorrectionLimit = d.CorrectionLimit
	}
	if cfg.LiveRetryBaseMillis == 0 {
		cfg.LiveRetryBaseMillis = d.LiveRetryBaseMillis
	}
	if cfg.LiveRetryMaxMillis == 0 {
		cfg.LiveRetryMaxMillis = d.LiveRetryMaxMillis
	}
	if cfg.WatchdogBudget.TokenCap == 0 {
		cfg.WatchdogBudget.TokenCap = d.WatchdogBudget.TokenCap
	}
	if cfg.WatchdogBudget.TimeoutSeconds == 0 {
		cfg.WatchdogBudget.TimeoutSeconds = d.WatchdogBudget.TimeoutSeconds
	}
	if cfg.VerifierBudget.TokenCap == 0 {
		cfg.VerifierBudget.TokenCap = d.VerifierBudget.TokenCap
	}
	if cfg.VerifierBudget.TimeoutSeconds == 0 {
		cfg.VerifierBudget.TimeoutSeconds = d.VerifierBudget.TimeoutSeconds
	}
	if cfg.Profile == ProfileHighAssurance {
		cfg.WatchdogRequired, cfg.VerifierRequired = true, true
		cfg.AllowAdvisoryFallback = false
		if cfg.WatchdogBudget.TokenCap < 64_000 {
			cfg.WatchdogBudget.TokenCap = 64_000
		}
		if cfg.VerifierBudget.TokenCap < 96_000 {
			cfg.VerifierBudget.TokenCap = 96_000
		}
	}
	if cfg.Mode != ModeEvent && cfg.Mode != ModeLive {
		return Config{}, fmt.Errorf("supervise: invalid mode %q", cfg.Mode)
	}
	if cfg.PivotApproval != PivotByUser && cfg.PivotApproval != PivotByWatchdog {
		return Config{}, fmt.Errorf("supervise: invalid pivot approval %q", cfg.PivotApproval)
	}
	if cfg.Profile != ProfileStandard && cfg.Profile != ProfileHighAssurance && cfg.Profile != ProfileCustom {
		return Config{}, fmt.Errorf("supervise: invalid profile %q", cfg.Profile)
	}
	if err := validateRoleProfile(cfg.Watchdog); err != nil {
		return Config{}, fmt.Errorf("supervise: watchdog: %w", err)
	}
	if err := validateRoleProfile(cfg.Verifier); err != nil {
		return Config{}, fmt.Errorf("supervise: verifier: %w", err)
	}
	if cfg.EventReviewRetries < 1 || cfg.EventReviewRetries > 10 || cfg.FailedEventLimit < 1 || cfg.FailedEventLimit > 100 || cfg.CorrectionLimit < 1 || cfg.CorrectionLimit > 20 {
		return Config{}, errors.New("supervise: retry or failure limit outside supported range")
	}
	if cfg.LiveRetryBaseMillis < 1 || cfg.LiveRetryMaxMillis < cfg.LiveRetryBaseMillis || cfg.LiveRetryMaxMillis > 300_000 {
		return Config{}, errors.New("supervise: invalid live retry backoff")
	}
	if err := validateBudget(cfg.WatchdogBudget); err != nil {
		return Config{}, fmt.Errorf("supervise: watchdog budget: %w", err)
	}
	if err := validateBudget(cfg.VerifierBudget); err != nil {
		return Config{}, fmt.Errorf("supervise: verifier budget: %w", err)
	}
	return cfg, nil
}

func validateRoleProfile(p RoleProfile) error {
	if p.Thinking != ThinkingAuto && p.Thinking != ThinkingOn && p.Thinking != ThinkingOff {
		return fmt.Errorf("invalid thinking mode %q", p.Thinking)
	}
	switch p.Effort {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
	default:
		return fmt.Errorf("invalid effort %q", p.Effort)
	}
	if p.ThinkingBudgetTokens < 0 || p.ThinkingBudgetTokens > 2_000_000 {
		return errors.New("thinking budget outside supported range")
	}
	if err := bounded(p.Provider, 512); err != nil {
		return err
	}
	if err := bounded(p.Model, 512); err != nil {
		return err
	}
	return nil
}

func validateBudget(b RoleBudget) error {
	if b.TokenCap < 0 || b.TimeoutSeconds < 0 || b.TimeoutSeconds > 86_400 {
		return errors.New("budget outside supported range")
	}
	return nil
}

func (s *Service) Create(_ context.Context, rootSession string, cfg Config, principal, actor, idem string) (State, error) {
	return s.create(rootSession, "", cfg, principal, actor, idem)
}

func (s *Service) CreateWithSeed(_ context.Context, rootSession, objectiveSeed string, cfg Config, principal, actor, idem string) (State, error) {
	return s.create(rootSession, objectiveSeed, cfg, principal, actor, idem)
}

func (s *Service) create(rootSession, objectiveSeed string, cfg Config, principal, actor, idem string) (State, error) {
	serviceMutationMu.Lock()
	defer serviceMutationMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(rootSession) == "" {
		return State{}, errors.New("supervise: root session required")
	}
	if err := bounded(objectiveSeed, 16_384); err != nil {
		return State{}, err
	}
	var err error
	if cfg, err = NormalizeConfig(cfg); err != nil {
		return State{}, err
	}
	if existing, ok, err := foldByRootSession(s.wal.Records(), rootSession); err != nil {
		return State{}, err
	} else if ok {
		if existing.Status != StatusCompleted && existing.Status != StatusCancelled {
			return State{}, errors.New("supervise: root session already has an active run")
		}
	}
	now := s.now().UTC()
	st := State{ID: mint("sup_"), Version: 1, Status: StatusSetup, RootSessionID: rootSession, AttachedSessionID: rootSession, ObjectiveSeed: strings.TrimSpace(objectiveSeed), Config: cfg, Detector: NewDetector(cfg.Mode).Snapshot(), CreatedAt: now, UpdatedAt: now}
	return st, s.append(st, principal, actor, idem, "run.created")
}

func (s *Service) State(id string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok, err := fold(s.wal.Records(), id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, ErrNotFound
	}
	return st, nil
}

// StateBySession returns the most recent run currently attached to session.
// Before the first recovery fork that session is also the immutable root.
func (s *Service) StateBySession(session string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok, err := foldByAttachedSession(s.wal.Records(), session)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, ErrNotFound
	}
	return st, nil
}

// ReattachSession moves a running supervision to an automatic recovery fork
// while retaining the immutable root session. The host must advance the
// runtime anchor at the same time, so any verdict produced for the parent
// session becomes stale before the child can execute.
func (s *Service) ReattachSession(_ context.Context, id string, expected uint64, role ActorRole, attachedSession string, anchor Anchor, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "runtime.reattached", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning {
			return ErrInvalidState
		}
		attachedSession = strings.TrimSpace(attachedSession)
		if attachedSession == "" {
			return errors.New("supervise: attached session required")
		}
		if err := bounded(attachedSession, 512); err != nil {
			return err
		}
		if err := validateRootAnchor(st.RootSessionID, anchor); err != nil {
			return err
		}
		if anchor.SessionSequence <= st.SessionSequence {
			return ErrStaleVerdict
		}
		if anchor.PlanVersion != 0 && anchor.PlanVersion != st.PlanVersion {
			return ErrStaleVerdict
		}
		if anchor.ActiveStep != "" && anchor.ActiveStep != st.ActiveStep {
			return ErrStaleVerdict
		}
		st.AttachedSessionID = attachedSession
		st.SessionSequence, st.TreeDigest = anchor.SessionSequence, anchor.TreeDigest
		st.LastVerdict, st.StepApproval = nil, nil
		return nil
	})
}

func (s *Service) ProposeBaseline(_ context.Context, id string, expected uint64, baseline Baseline, role ActorRole, anchor Anchor, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "baseline.proposed", func(st *State) error {
		if role != RoleWatchdog && role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusSetup && st.Status != StatusAwaitingApproval {
			return ErrInvalidState
		}
		if err := validateBaseline(baseline); err != nil {
			return err
		}
		if err := validateRootAnchor(st.RootSessionID, anchor); err != nil {
			return err
		}
		st.Baseline = baseline
		st.SessionSequence, st.TreeDigest = anchor.SessionSequence, anchor.TreeDigest
		st.Status = StatusAwaitingApproval
		st.PauseReason = ""
		return nil
	})
}

func (s *Service) ApproveBaseline(_ context.Context, id string, expected uint64, role ActorRole, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "baseline.approved", func(st *State) error {
		if role != RoleOperator {
			return ErrUnauthorized
		}
		if st.Status != StatusAwaitingApproval {
			return ErrInvalidState
		}
		if err := validateBaseline(st.Baseline); err != nil {
			return err
		}
		st.ContractDigest = baselineDigest(st.Baseline)
		st.PlanVersion++
		st.ActiveStep = st.Baseline.Plan[0].ID
		st.Status = StatusRunning
		return nil
	})
}

func (s *Service) UpdateRuntimeAnchor(_ context.Context, id string, expected uint64, role ActorRole, anchor Anchor, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "runtime.advanced", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status == StatusCompleted || st.Status == StatusCancelled {
			return ErrInvalidState
		}
		if err := validateRootAnchor(st.RootSessionID, anchor); err != nil {
			return err
		}
		if anchor.SessionSequence < st.SessionSequence {
			return ErrStaleVerdict
		}
		if anchor.PlanVersion != 0 && anchor.PlanVersion != st.PlanVersion {
			return ErrStaleVerdict
		}
		if anchor.ActiveStep != "" && anchor.ActiveStep != st.ActiveStep {
			return ErrStaleVerdict
		}
		if st.InitialTreeDigest == "" {
			st.InitialTreeDigest = anchor.TreeDigest
		}
		st.SessionSequence, st.TreeDigest = anchor.SessionSequence, anchor.TreeDigest
		return nil
	})
}

func (s *Service) RecordDetectorSnapshot(_ context.Context, id string, expected uint64, role ActorRole, snapshot DetectorSnapshot, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "detector.snapshot", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status == StatusCompleted || st.Status == StatusCancelled {
			return ErrInvalidState
		}
		if err := validateDetectorSnapshot(snapshot, st.Config.Mode); err != nil {
			return err
		}
		st.Detector = snapshot
		return nil
	})
}

func (s *Service) RecordEvidence(_ context.Context, id string, expected uint64, role ActorRole, e Evidence, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "evidence.recorded", func(st *State) error {
		if role != RoleWorker && role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning && st.Status != StatusPivotPending {
			return ErrInvalidState
		}
		if err := anchorsMatch(st.Anchor(), e.Anchor); err != nil {
			return err
		}
		if err := validateEvidence(e); err != nil {
			return err
		}
		if len(st.Evidence) >= 512 {
			return errors.New("supervise: evidence limit reached")
		}
		e.CreatedAt = s.now().UTC()
		st.Evidence = append(st.Evidence, e)
		return nil
	})
}

// EnqueueFollowup durably captures operator input before the UI acknowledges
// it as queued. This prevents prompts entered during a long-running turn from
// disappearing if stado exits before the watchdog can classify them.
func (s *Service) EnqueueFollowup(_ context.Context, id string, expected uint64, role ActorRole, text, principal, actor, idem string) (State, Followup, error) {
	var followup Followup
	st, err := s.mutate(id, expected, principal, actor, idem, "followup.enqueued", func(st *State) error {
		if role != RoleOperator {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning && st.Status != StatusPivotPending && st.Status != StatusVerifying && st.Status != StatusPaused {
			return ErrInvalidState
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return errors.New("supervise: follow-up text required")
		}
		if err := bounded(text, 4096); err != nil {
			return err
		}
		if len(st.PendingFollowups) >= 64 {
			return errors.New("supervise: pending follow-up limit reached")
		}
		followup = Followup{ID: mint("followup_"), Text: text, RequestedAt: s.now().UTC()}
		st.PendingFollowups = append(st.PendingFollowups, followup)
		return nil
	})
	return st, followup, err
}

// ResolveFollowup removes a queued item only after the host has routed it to
// durable conversation history or created the corresponding durable task.
func (s *Service) ResolveFollowup(_ context.Context, id string, expected uint64, role ActorRole, followupID, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "followup.resolved", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning {
			return ErrInvalidState
		}
		idx := -1
		for i := range st.PendingFollowups {
			if st.PendingFollowups[i].ID == followupID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		st.PendingFollowups = append(st.PendingFollowups[:idx], st.PendingFollowups[idx+1:]...)
		return nil
	})
}

func (s *Service) AdvanceStep(_ context.Context, id string, expected uint64, role ActorRole, anchor Anchor, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "plan.step_advanced", func(st *State) error {
		if role != RoleWatchdog && role != RoleOperator {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning {
			return ErrInvalidState
		}
		if err := anchorsMatch(st.Anchor(), anchor); err != nil {
			return err
		}
		if role == RoleWatchdog {
			if st.LastVerdict == nil || st.LastVerdict.Kind != ReviewEvent || st.LastVerdict.Decision != VerdictApprove || st.StepApproval == nil {
				return errors.New("supervise: watchdog step transition requires an evidence-backed event approval")
			}
			if err := anchorsMatch(st.Anchor(), st.LastVerdict.Anchor); err != nil {
				return err
			}
			if err := anchorsMatch(st.Anchor(), st.StepApproval.Anchor); err != nil {
				return err
			}
		}
		idx := stepIndex(st.Baseline.Plan, st.ActiveStep)
		if idx < 0 {
			return errors.New("supervise: active step not found")
		}
		if !contains(st.CompletedSteps, st.ActiveStep) {
			st.CompletedSteps = append(st.CompletedSteps, st.ActiveStep)
		}
		if idx+1 < len(st.Baseline.Plan) {
			st.ActiveStep = st.Baseline.Plan[idx+1].ID
		} else {
			st.ActiveStep = ""
		}
		st.StepApproval = nil
		return nil
	})
}

func (s *Service) RequestPivot(_ context.Context, id string, expected uint64, role ActorRole, req PivotRequest, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "pivot.requested", func(st *State) error {
		if role != RoleWorker {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning || st.PendingIntervention != nil {
			return ErrInvalidState
		}
		if req.Kind == PivotTactical {
			return errors.New("supervise: tactical changes do not require pivot approval")
		}
		if req.Kind != PivotPlan && req.Kind != PivotContract {
			return errors.New("supervise: invalid pivot kind")
		}
		if err := anchorsMatch(st.Anchor(), req.Anchor); err != nil {
			return err
		}
		if err := bounded(req.Reason, 4096); err != nil {
			return err
		}
		if strings.TrimSpace(req.Reason) == "" {
			return errors.New("supervise: pivot reason required")
		}
		if req.Kind == PivotPlan {
			if err := validatePlan(req.ProposedPlan); err != nil {
				return err
			}
		} else {
			if req.ProposedBaseline == nil {
				return errors.New("supervise: contract pivot requires a proposed baseline")
			}
			if err := validateBaseline(*req.ProposedBaseline); err != nil {
				return err
			}
		}
		// IDs are host-minted even for direct callers; model-selected IDs must
		// not control audit identity or collide with an earlier request.
		req.ID = mint("pivot_")
		req.RequestedAt = s.now().UTC()
		st.PendingPivot = &req
		st.Status = StatusPivotPending
		return nil
	})
}

func (s *Service) ResolvePivot(_ context.Context, id string, expected uint64, role ActorRole, verdict Verdict, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "pivot.resolved", func(st *State) error {
		if st.Status != StatusPivotPending || st.PendingPivot == nil {
			return ErrInvalidState
		}
		if verdict.Kind != ReviewPivot || (verdict.Decision != VerdictApprove && verdict.Decision != VerdictReject) {
			return errors.New("supervise: invalid pivot verdict")
		}
		if err := validateVerdict(verdict); err != nil {
			return err
		}
		if err := anchorsMatch(st.Anchor(), verdict.Anchor); err != nil {
			return err
		}
		if err := anchorsMatch(st.PendingPivot.Anchor, verdict.Anchor); err != nil {
			return err
		}
		if role != RoleOperator {
			if role != RoleWatchdog || st.Config.PivotApproval != PivotByWatchdog || st.PendingPivot.Kind != PivotPlan {
				return ErrUnauthorized
			}
			if verdict.Decision == VerdictApprove && len(verdict.EvidenceRefs) == 0 {
				return errors.New("supervise: watchdog pivot approval requires evidence references")
			}
		}
		verdict.ReviewedAt = s.now().UTC()
		st.LastVerdict, st.WatchdogHandoff = &verdict, verdict.Handoff
		if verdict.Decision == VerdictApprove {
			if st.PendingPivot.Kind == PivotContract {
				if role != RoleOperator {
					return ErrUnauthorized
				}
				st.Baseline = *st.PendingPivot.ProposedBaseline
				st.ContractDigest = baselineDigest(st.Baseline)
			} else {
				st.Baseline.Plan = append([]Step(nil), st.PendingPivot.ProposedPlan...)
			}
			st.ContractDigest = baselineDigest(st.Baseline)
			st.PlanVersion++
			st.CompletedSteps = nil
			st.ActiveStep = st.Baseline.Plan[0].ID
		}
		st.PendingPivot = nil
		st.Status = StatusRunning
		return nil
	})
}

func (s *Service) RequestCompletion(_ context.Context, id string, expected uint64, role ActorRole, req CompletionRequest, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "completion.requested", func(st *State) error {
		if role != RoleWorker {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning || st.PendingIntervention != nil {
			return ErrInvalidState
		}
		if err := anchorsMatch(st.Anchor(), req.Anchor); err != nil {
			return err
		}
		if st.ActiveStep != "" || len(st.CompletedSteps) != len(st.Baseline.Plan) {
			return errors.New("supervise: every approved plan step must complete before final verification")
		}
		if err := bounded(req.Summary, 4096); err != nil {
			return err
		}
		if strings.TrimSpace(req.Summary) == "" || len(req.Evidence) == 0 || len(req.Evidence) > 128 {
			return errors.New("supervise: completion summary and evidence required")
		}
		for _, e := range req.Evidence {
			if err := anchorsMatch(req.Anchor, e.Anchor); err != nil {
				return err
			}
			if err := validateEvidence(e); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		for i := range req.Evidence {
			req.Evidence[i].CreatedAt = now
		}
		req.RequestedAt = now
		st.Completion = &req
		st.Status = StatusVerifying
		return nil
	})
}

func (s *Service) ResolveCompletion(_ context.Context, id string, expected uint64, role ActorRole, verdict Verdict, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "completion.resolved", func(st *State) error {
		if role != RoleVerifier {
			return ErrUnauthorized
		}
		if st.Status != StatusVerifying || st.Completion == nil {
			return ErrInvalidState
		}
		if verdict.Kind != ReviewCompletion || (verdict.Decision != VerdictApprove && verdict.Decision != VerdictReject) {
			return errors.New("supervise: invalid completion verdict")
		}
		if err := validateVerdict(verdict); err != nil {
			return err
		}
		if err := anchorsMatch(st.Anchor(), verdict.Anchor); err != nil {
			return err
		}
		if err := anchorsMatch(st.Completion.Anchor, verdict.Anchor); err != nil {
			return err
		}
		if verdict.Decision == VerdictApprove && len(verdict.EvidenceRefs) == 0 {
			return errors.New("supervise: completion approval requires evidence references")
		}
		verdict.ReviewedAt = s.now().UTC()
		st.LastVerdict = &verdict
		if verdict.Decision == VerdictApprove {
			st.Status = StatusCompleted
		} else {
			st.Status = StatusRunning
			st.Completion = nil
		}
		return nil
	})
}

// RecordVerificationGate records a deterministic host-run gate before the
// independent verifier. A failed gate rejects the worker's completion request
// and returns the run to execution; a passed gate preserves verifying status.
func (s *Service) RecordVerificationGate(_ context.Context, id string, expected uint64, role ActorRole, passed bool, summary string, refs []string, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "completion.gate", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusVerifying || st.Completion == nil {
			return ErrInvalidState
		}
		evidence := Evidence{Kind: "verification_gate", Summary: summary, References: append([]string(nil), refs...), Anchor: st.Anchor(), CreatedAt: s.now().UTC()}
		if err := validateEvidence(evidence); err != nil {
			return err
		}
		if len(st.Evidence) >= 512 {
			return errors.New("supervise: evidence limit reached")
		}
		st.Evidence = append(st.Evidence, evidence)
		if !passed {
			st.Status, st.Completion = StatusRunning, nil
		}
		return nil
	})
}

// InvalidateCompletion fails closed when host-observed state changes while a
// verifier is running, or when an explicitly optional verifier is unavailable.
// It never accepts completion; it returns the run to execution with evidence.
func (s *Service) InvalidateCompletion(_ context.Context, id string, expected uint64, role ActorRole, reason, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "completion.invalidated", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusVerifying || st.Completion == nil {
			return ErrInvalidState
		}
		evidence := Evidence{Kind: "completion_invalidated", Summary: strings.TrimSpace(reason), Anchor: st.Anchor(), CreatedAt: s.now().UTC()}
		if err := validateEvidence(evidence); err != nil {
			return err
		}
		if len(st.Evidence) >= 512 {
			return errors.New("supervise: evidence limit reached")
		}
		st.Evidence = append(st.Evidence, evidence)
		st.Status, st.Completion = StatusRunning, nil
		return nil
	})
}

func (s *Service) Pause(_ context.Context, id string, expected uint64, role ActorRole, reason, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "run.paused", func(st *State) error {
		// Watchdog pauses must arrive as evidence-bound event verdicts. This
		// direct transition is reserved for the trusted host and operator.
		if role != RoleHost && role != RoleOperator {
			return ErrUnauthorized
		}
		if st.Status == StatusCompleted || st.Status == StatusCancelled || st.Status == StatusPaused {
			return ErrInvalidState
		}
		if strings.TrimSpace(reason) == "" {
			return errors.New("supervise: pause reason required")
		}
		if err := bounded(reason, 4096); err != nil {
			return err
		}
		st.ResumeStatus, st.Status, st.PauseReason = st.Status, StatusPaused, reason
		return nil
	})
}

func (s *Service) RecordEventVerdict(_ context.Context, id string, expected uint64, role ActorRole, verdict Verdict, trigger *Trigger, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "watchdog.verdict", func(st *State) error {
		if role != RoleWatchdog {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning && st.Status != StatusPivotPending {
			return ErrInvalidState
		}
		if err := validateVerdict(verdict); err != nil {
			return err
		}
		if verdict.Kind != ReviewEvent {
			return errors.New("supervise: event verdict required")
		}
		if err := validateTrigger(trigger); err != nil {
			return err
		}
		if verdictNeedsEvidence(verdict.Kind, verdict.Decision) && len(verdict.EvidenceRefs) == 0 {
			return errors.New("supervise: watchdog intervention requires evidence references")
		}
		if err := anchorsMatch(st.Anchor(), verdict.Anchor); err != nil {
			return err
		}
		if err := anchorsMatch(st.Anchor(), trigger.Anchor); err != nil {
			return err
		}
		if st.PendingIntervention != nil {
			confirmsHold := false
			for _, signal := range trigger.Signals {
				if signal.Type == TriggerStaleIntervention && signal.Attributes["intervention_id"] == st.PendingIntervention.ID {
					confirmsHold = true
					break
				}
			}
			if !confirmsHold {
				return errors.New("supervise: intervention hold requires a fresh stale-intervention review")
			}
		}
		verdict.ReviewedAt = s.now().UTC()
		// A current-anchor verdict is the only watchdog judgment that can
		// release a hold created by an earlier stale pause/stop proposal.
		st.PendingIntervention = nil
		st.LastVerdict, st.WatchdogHandoff = &verdict, verdict.Handoff
		st.StepApproval = nil
		if verdict.Decision == VerdictApprove {
			if evidenceRef, ok := stepApprovalEvidence(st.Anchor(), trigger, verdict.EvidenceRefs); ok {
				st.StepApproval = &StepApproval{Anchor: st.Anchor(), TriggerID: trigger.ID, EvidenceRef: evidenceRef}
			}
		}
		st.FailedEventStreak = 0
		switch verdict.Decision {
		case VerdictContinue, VerdictApprove:
			st.FailedCorrectionCount = 0
		case VerdictCorrect:
			if strings.TrimSpace(verdict.Correction) == "" {
				return errors.New("supervise: correction verdict requires a prompt")
			}
		case VerdictPause, VerdictStop:
			st.ResumeStatus, st.Status, st.PauseReason = st.Status, StatusPaused, verdict.Rationale
		default:
			return errors.New("supervise: invalid event verdict")
		}
		return nil
	})
}

// HoldStaleIntervention persists a stale pause/stop proposal without applying
// it as a verdict. The host uses this as a scheduling barrier while it asks a
// fresh watchdog to judge the current anchor. This is deliberately distinct
// from RecordEventVerdict: stale model output can demand another look, but it
// cannot itself pause the durable run or acquire current-state authority.
func (s *Service) HoldStaleIntervention(_ context.Context, id string, expected uint64, role ActorRole, verdict Verdict, trigger *Trigger, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "watchdog.intervention_held", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning {
			return ErrInvalidState
		}
		if verdict.Decision != VerdictPause && verdict.Decision != VerdictStop {
			return errors.New("supervise: only stale pause/stop verdicts create an intervention hold")
		}
		if err := validateVerdict(verdict); err != nil {
			return err
		}
		if len(verdict.EvidenceRefs) == 0 {
			return errors.New("supervise: stale pause/stop hold requires evidence references")
		}
		if err := validateTrigger(trigger); err != nil {
			return err
		}
		if err := anchorsMatch(verdict.Anchor, trigger.Anchor); err != nil {
			return err
		}
		if err := validateRootAnchor(st.RootSessionID, verdict.Anchor); err != nil {
			return err
		}
		if verdict.Anchor == st.Anchor() {
			return errors.New("supervise: current pause/stop verdict must use RecordEventVerdict")
		}
		now := s.now().UTC()
		verdict.ReviewedAt = now
		st.PendingIntervention = &InterventionHold{ID: mint("intervention_"), Verdict: verdict, Trigger: *trigger, HeldAt: now}
		return nil
	})
}

func stepApprovalEvidence(anchor Anchor, trigger *Trigger, verdictRefs []string) (string, bool) {
	if trigger == nil || trigger.Anchor != anchor || strings.TrimSpace(trigger.ID) == "" {
		return "", false
	}
	cited := make(map[string]bool, len(verdictRefs))
	for _, ref := range verdictRefs {
		cited[ref] = true
	}
	for _, signal := range trigger.Signals {
		if signal.Type != TriggerStepCompletion {
			continue
		}
		// Coalesced detector signals may be retried against a newer anchor.
		// Evidence for an earlier step must never become authority for the
		// currently active step merely because the host refreshed the trigger.
		if strings.TrimSpace(signal.Attributes["step"]) != anchor.ActiveStep {
			continue
		}
		for _, ref := range signal.EvidenceRefs {
			if cited[ref] {
				return ref, true
			}
		}
	}
	return "", false
}

// RecordReviewFailure is called only after the configured per-review retries
// have been exhausted. Event mode may continue advisory work but pauses after
// a bounded consecutive streak; required/no-fallback review pauses immediately.
// Live mode normally retries until success or operator interruption, and an
// interrupted live review pauses here. A pivot can never be approved here.
func (s *Service) RecordReviewFailure(_ context.Context, id string, expected uint64, role ActorRole, reason, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "watchdog.review_failed", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if err := bounded(reason, 4096); err != nil {
			return err
		}
		if st.Status != StatusRunning && st.Status != StatusPivotPending {
			return ErrInvalidState
		}
		st.FailedEventStreak++
		if st.PendingIntervention != nil || st.Config.Mode == ModeLive || st.FailedEventStreak >= st.Config.FailedEventLimit || st.Config.WatchdogRequired || !st.Config.AllowAdvisoryFallback {
			st.ResumeStatus, st.Status, st.PauseReason = st.Status, StatusPaused, strings.TrimSpace(reason)
			if st.PauseReason == "" {
				st.PauseReason = "watchdog unavailable after retries"
			}
		}
		return nil
	})
}

func (s *Service) RecordCorrectionResult(_ context.Context, id string, expected uint64, role ActorRole, success bool, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "watchdog.correction_result", func(st *State) error {
		if role != RoleHost {
			return ErrUnauthorized
		}
		if st.Status != StatusRunning {
			return ErrInvalidState
		}
		if success {
			st.FailedCorrectionCount = 0
			return nil
		}
		st.FailedCorrectionCount++
		if st.FailedCorrectionCount >= st.Config.CorrectionLimit {
			st.ResumeStatus, st.Status, st.PauseReason = st.Status, StatusPaused, "watchdog corrections did not restore alignment"
		}
		return nil
	})
}

func (s *Service) Resume(_ context.Context, id string, expected uint64, role ActorRole, anchor Anchor, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "run.resumed", func(st *State) error {
		if role != RoleOperator {
			return ErrUnauthorized
		}
		if st.Status != StatusPaused {
			return ErrInvalidState
		}
		if err := validateRootAnchor(st.RootSessionID, anchor); err != nil {
			return err
		}
		target := st.ResumeStatus
		if target != StatusRunning && target != StatusPivotPending && target != StatusVerifying {
			target = StatusRunning
		}
		st.SessionSequence, st.TreeDigest = anchor.SessionSequence, anchor.TreeDigest
		// A paused authority-bearing request may resume only if its original
		// anchor still describes the worker. Changed trees invalidate the request
		// and safely return to execution instead of creating an impossible stale
		// verdict loop.
		if target == StatusPivotPending && (st.PendingPivot == nil || st.PendingPivot.Anchor != st.Anchor()) {
			st.PendingPivot = nil
			target = StatusRunning
		}
		if target == StatusVerifying && (st.Completion == nil || st.Completion.Anchor != st.Anchor()) {
			st.Completion = nil
			target = StatusRunning
		}
		st.FailedEventStreak, st.FailedCorrectionCount = 0, 0
		// An explicit operator resume supersedes a pending stale model
		// proposal. The prior pause reason is still delivered to the worker by
		// the TUI, while model-originated holds can never out-rank the operator.
		st.PendingIntervention = nil
		st.PauseReason, st.ResumeStatus, st.Status = "", "", target
		return nil
	})
}

func (s *Service) Cancel(_ context.Context, id string, expected uint64, role ActorRole, principal, actor, idem string) (State, error) {
	return s.mutate(id, expected, principal, actor, idem, "run.cancelled", func(st *State) error {
		if role != RoleOperator {
			return ErrUnauthorized
		}
		if st.Status == StatusCompleted || st.Status == StatusCancelled {
			return ErrInvalidState
		}
		st.Status = StatusCancelled
		return nil
	})
}

func (s *Service) mutate(id string, expected uint64, principal, actor, idem, typ string, fn func(*State) error) (State, error) {
	serviceMutationMu.Lock()
	defer serviceMutationMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok, err := fold(s.wal.Records(), id)
	if err != nil {
		return State{}, err
	}
	if !ok {
		return State{}, ErrNotFound
	}
	if st.Version != expected {
		return State{}, ErrVersion
	}
	if err := fn(&st); err != nil {
		return State{}, err
	}
	st.Version++
	st.UpdatedAt = s.now().UTC()
	return st, s.append(st, principal, actor, idem, typ)
}

func (s *Service) append(st State, principal, actor, idem, typ string) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(b) > maxStateBytes {
		return fmt.Errorf("supervise: durable state exceeds %d bytes", maxStateBytes)
	}
	_, err = s.wal.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: storeName, Type: typ, Session: st.RootSessionID, Data: b}}})
	return err
}

func fold(records []wal.Record, id string) (State, bool, error) {
	var st State
	found := false
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName {
				continue
			}
			var next State
			if err := json.Unmarshal(ev.Data, &next); err != nil {
				return State{}, false, err
			}
			if next.ID != id {
				continue
			}
			if found && next.Version != st.Version+1 {
				return State{}, false, ErrVersion
			}
			st, found = next, true
		}
	}
	return st, found, nil
}

func foldByAttachedSession(records []wal.Record, session string) (State, bool, error) {
	return foldLatestMatching(records, func(st State) bool {
		attached := st.AttachedSessionID
		if attached == "" { // migration path for records written before v0.80.0
			attached = st.RootSessionID
		}
		return attached == session
	})
}

func foldByRootSession(records []wal.Record, session string) (State, bool, error) {
	return foldLatestMatching(records, func(st State) bool {
		return st.RootSessionID == session
	})
}

func foldLatestMatching(records []wal.Record, matches func(State) bool) (State, bool, error) {
	states := map[string]State{}
	orders := map[string]int{}
	versions := map[string]uint64{}
	order := 0
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName {
				continue
			}
			var st State
			if err := json.Unmarshal(ev.Data, &st); err != nil {
				return State{}, false, err
			}
			if version := versions[st.ID]; version != 0 && st.Version != version+1 {
				return State{}, false, ErrVersion
			}
			versions[st.ID] = st.Version
			states[st.ID], orders[st.ID] = st, order
			order++
		}
	}
	var latest State
	latestOrder := -1
	for id, st := range states {
		if matches(st) && orders[id] > latestOrder {
			latest, latestOrder = st, orders[id]
		}
	}
	if latest.ID == "" {
		return State{}, false, nil
	}
	return latest, true, nil
}

func validateBaseline(b Baseline) error {
	if strings.TrimSpace(b.Objective) == "" {
		return errors.New("supervise: objective required")
	}
	if err := bounded(b.Objective, 4096); err != nil {
		return err
	}
	if len(b.AcceptanceCriteria) == 0 || len(b.DefinitionOfDone) == 0 || len(b.Verification) == 0 {
		return errors.New("supervise: acceptance criteria, definition of done, and verification are required")
	}
	for _, list := range [][]string{b.Constraints, b.NonGoals, b.AcceptanceCriteria, b.DefinitionOfDone, b.Verification, b.Risks} {
		if len(list) > 64 {
			return errors.New("supervise: baseline list exceeds 64 items")
		}
		for _, v := range list {
			if strings.TrimSpace(v) == "" {
				return errors.New("supervise: empty baseline item")
			}
			if err := bounded(v, 2048); err != nil {
				return err
			}
		}
	}
	return validatePlan(b.Plan)
}

func validatePlan(plan []Step) error {
	if len(plan) == 0 || len(plan) > 128 {
		return errors.New("supervise: plan must contain 1 to 128 steps")
	}
	seen := map[string]bool{}
	for _, step := range plan {
		if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.Title) == "" || strings.TrimSpace(step.DoneWhen) == "" {
			return errors.New("supervise: each plan step requires id, title, and done_when")
		}
		if seen[step.ID] {
			return errors.New("supervise: duplicate plan step id")
		}
		seen[step.ID] = true
		if err := bounded(step.ID, 64); err != nil {
			return err
		}
		if err := bounded(step.Title, 1024); err != nil {
			return err
		}
		if err := bounded(step.DoneWhen, 2048); err != nil {
			return err
		}
	}
	return nil
}

func validateEvidence(e Evidence) error {
	if strings.TrimSpace(e.Kind) == "" || strings.TrimSpace(e.Summary) == "" {
		return errors.New("supervise: evidence kind and summary required")
	}
	if err := bounded(e.Kind, 64); err != nil {
		return err
	}
	if err := bounded(e.Summary, 2048); err != nil {
		return err
	}
	if len(e.References) > 64 {
		return errors.New("supervise: evidence reference limit reached")
	}
	for _, ref := range e.References {
		if strings.TrimSpace(ref) == "" {
			return errors.New("supervise: empty evidence reference")
		}
		if err := bounded(ref, 2048); err != nil {
			return err
		}
	}
	return nil
}

func validateDetectorSnapshot(snapshot DetectorSnapshot, mode Mode) error {
	if snapshot.Mode != mode || len(snapshot.History) > 32 {
		return errors.New("supervise: invalid detector snapshot")
	}
	if len(snapshot.LastEmittedAt) > maxDetectorCooldowns || len(snapshot.LastEmittedSeq) > maxDetectorCooldowns {
		return errors.New("supervise: detector cooldown map exceeds limit")
	}
	for _, ev := range snapshot.History {
		if !workerEventWithinBounds(ev) {
			return errors.New("supervise: detector event exceeds durable evidence limits")
		}
	}
	for key := range snapshot.LastEmittedAt {
		if err := bounded(key, 8192); err != nil {
			return err
		}
	}
	for key := range snapshot.LastEmittedSeq {
		if err := bounded(key, 8192); err != nil {
			return err
		}
	}
	return nil
}

func validateTrigger(trigger *Trigger) error {
	if trigger == nil || strings.TrimSpace(trigger.ID) == "" || len(trigger.Signals) == 0 || len(trigger.Signals) > 32 {
		return errors.New("supervise: invalid review trigger")
	}
	if err := bounded(trigger.ID, 256); err != nil {
		return err
	}
	for _, signal := range trigger.Signals {
		switch signal.Type {
		case TriggerLiveTurn, TriggerRepeatedFailure, TriggerRetryThrash, TriggerEditRevert,
			TriggerVerificationRegress, TriggerNoProgress, TriggerBudgetBurn,
			TriggerScopeExpansion, TriggerChildFailure, TriggerStepCompletion,
			TriggerPivot, TriggerRisk, TriggerCompletion, TriggerCorrectionFollowup,
			TriggerStaleIntervention:
		default:
			return errors.New("supervise: invalid review trigger signal")
		}
		if signal.Severity != "info" && signal.Severity != "warning" && signal.Severity != "high" && signal.Severity != "critical" {
			return errors.New("supervise: invalid review trigger severity")
		}
		if len(signal.EvidenceRefs) == 0 || len(signal.EvidenceRefs) > 64 || len(signal.Attributes) > 16 {
			return errors.New("supervise: review trigger evidence exceeds limit")
		}
		for _, ref := range signal.EvidenceRefs {
			if strings.TrimSpace(ref) == "" {
				return errors.New("supervise: empty review trigger evidence reference")
			}
			if err := bounded(ref, 256); err != nil {
				return err
			}
		}
		for key, value := range signal.Attributes {
			if strings.TrimSpace(key) == "" {
				return errors.New("supervise: empty review trigger attribute")
			}
			if err := bounded(key, 256); err != nil {
				return err
			}
			if err := bounded(value, 4096); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRootAnchor(root string, anchor Anchor) error {
	if anchor.RootSessionID != root || strings.TrimSpace(anchor.TreeDigest) == "" {
		return errors.New("supervise: invalid root anchor")
	}
	return nil
}

func anchorsMatch(want, got Anchor) error {
	if want != got {
		return ErrStaleVerdict
	}
	return nil
}

func baselineDigest(b Baseline) string {
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stepIndex(plan []Step, id string) int {
	for i := range plan {
		if plan[i].ID == id {
			return i
		}
	}
	return -1
}
func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
func bounded(v string, n int) error {
	if utf8.RuneCountInString(v) > n {
		return fmt.Errorf("supervise: field exceeds %d characters", n)
	}
	return nil
}
func mint(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
