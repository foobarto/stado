package supervise

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDetectorPaths      = 64
	maxDetectorPathRunes  = 1024
	maxDetectorFieldRunes = 4096
	maxDetectorCooldowns  = 256
)

type WorkerEventKind string

const (
	WorkerTurnCompleted      WorkerEventKind = "turn_completed"
	WorkerToolOutcome        WorkerEventKind = "tool_outcome"
	WorkerVerification       WorkerEventKind = "verification"
	WorkerTreeChanged        WorkerEventKind = "tree_changed"
	WorkerChildLifecycle     WorkerEventKind = "child_lifecycle"
	WorkerStepClaimed        WorkerEventKind = "step_completion_claimed"
	WorkerPivotRequested     WorkerEventKind = "pivot_requested"
	WorkerRiskBoundary       WorkerEventKind = "risk_boundary"
	WorkerCompletionClaimed  WorkerEventKind = "completion_claimed"
	WorkerCorrectionFollowup WorkerEventKind = "correction_followup"
)

// WorkerEvent contains host-observed facts only. Free-form model reasoning is
// deliberately absent; the watchdog may interpret the bounded evidence later.
type WorkerEvent struct {
	ID                 string          `json:"id"`
	Kind               WorkerEventKind `json:"kind"`
	Sequence           uint64          `json:"sequence"`
	At                 time.Time       `json:"at"`
	StepID             string          `json:"step_id,omitempty"`
	Tool               string          `json:"tool,omitempty"`
	ArgsDigest         string          `json:"args_digest,omitempty"`
	OutcomeDigest      string          `json:"outcome_digest,omitempty"`
	ErrorFingerprint   string          `json:"error_fingerprint,omitempty"`
	Succeeded          bool            `json:"succeeded,omitempty"`
	VerificationPassed *bool           `json:"verification_passed,omitempty"`
	TreeDigest         string          `json:"tree_digest,omitempty"`
	DiffBytes          int64           `json:"diff_bytes,omitempty"`
	ChangedPaths       []string        `json:"changed_paths,omitempty"`
	OutOfScopePaths    []string        `json:"out_of_scope_paths,omitempty"`
	CompletedSteps     int             `json:"completed_steps,omitempty"`
	EvidenceCount      int             `json:"evidence_count,omitempty"`
	// CriteriaCompleted is retained only to decode detector snapshots written
	// before completed-step progress became explicit.
	CriteriaCompleted int    `json:"criteria_completed,omitempty"`
	TokenUsage        uint64 `json:"token_usage,omitempty"`
	TokenBudget       uint64 `json:"token_budget,omitempty"`
	ChildID           string `json:"child_id,omitempty"`
	ChildStatus       string `json:"child_status,omitempty"`
	Boundary          string `json:"boundary,omitempty"`
}

type TriggerType string

const (
	TriggerLiveTurn            TriggerType = "live_turn"
	TriggerRepeatedFailure     TriggerType = "repeated_failure"
	TriggerRetryThrash         TriggerType = "retry_thrash"
	TriggerEditRevert          TriggerType = "edit_revert_cycle"
	TriggerVerificationRegress TriggerType = "verification_regression"
	TriggerNoProgress          TriggerType = "no_criteria_progress"
	TriggerBudgetBurn          TriggerType = "budget_burn"
	TriggerScopeExpansion      TriggerType = "scope_expansion"
	TriggerChildFailure        TriggerType = "child_failure"
	TriggerStepCompletion      TriggerType = "step_completion_claim"
	TriggerPivot               TriggerType = "pivot_request"
	TriggerRisk                TriggerType = "risky_boundary"
	TriggerCompletion          TriggerType = "completion_claim"
	TriggerCorrectionFollowup  TriggerType = "correction_followup"
	TriggerStaleIntervention   TriggerType = "stale_intervention"
)

type TriggerSignal struct {
	Type         TriggerType       `json:"type"`
	Severity     string            `json:"severity"`
	EvidenceRefs []string          `json:"evidence_refs"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

type Trigger struct {
	ID        string          `json:"id"`
	Anchor    Anchor          `json:"anchor"`
	Signals   []TriggerSignal `json:"signals"`
	CreatedAt time.Time       `json:"created_at"`
}

type DetectorSnapshot struct {
	Mode           Mode                 `json:"mode"`
	History        []WorkerEvent        `json:"history,omitempty"`
	LastEmittedAt  map[string]time.Time `json:"last_emitted_at,omitempty"`
	LastEmittedSeq map[string]uint64    `json:"last_emitted_sequence,omitempty"`
}

type Detector struct {
	snapshot DetectorSnapshot
	cooldown time.Duration
	now      func() time.Time
}

func NewDetector(mode Mode) *Detector {
	if mode != ModeLive {
		mode = ModeEvent
	}
	return &Detector{snapshot: DetectorSnapshot{Mode: mode, LastEmittedAt: map[string]time.Time{}, LastEmittedSeq: map[string]uint64{}}, cooldown: 90 * time.Second, now: time.Now}
}

func RestoreDetector(snapshot DetectorSnapshot) *Detector {
	if snapshot.Mode != ModeLive {
		snapshot.Mode = ModeEvent
	}
	d := NewDetector(snapshot.Mode)
	d.snapshot = snapshot
	if d.snapshot.LastEmittedAt == nil {
		d.snapshot.LastEmittedAt = map[string]time.Time{}
	}
	if d.snapshot.LastEmittedSeq == nil {
		d.snapshot.LastEmittedSeq = map[string]uint64{}
	}
	if len(d.snapshot.History) > 32 {
		d.snapshot.History = append([]WorkerEvent(nil), d.snapshot.History[len(d.snapshot.History)-32:]...)
	}
	return d
}

func (d *Detector) Snapshot() DetectorSnapshot {
	out := d.snapshot
	out.History = append([]WorkerEvent(nil), d.snapshot.History...)
	out.LastEmittedAt = cloneTimes(d.snapshot.LastEmittedAt)
	out.LastEmittedSeq = cloneSeqs(d.snapshot.LastEmittedSeq)
	return out
}

// Observe returns at most one coalesced review trigger. In event mode an
// ordinary turn with no detector hit stays silent; live mode adds a turn
// boundary signal so the watchdog follows every completed worker turn.
func (d *Detector) Observe(ev WorkerEvent, anchor Anchor) *Trigger {
	ev = boundWorkerEvent(ev)
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("worker-event:%d", ev.Sequence)
	}
	if ev.At.IsZero() {
		ev.At = d.now().UTC()
	}
	prior := append([]WorkerEvent(nil), d.snapshot.History...)
	signals := d.detect(prior, ev)
	if d.snapshot.Mode == ModeLive && ev.Kind == WorkerTurnCompleted {
		signals = append(signals, TriggerSignal{Type: TriggerLiveTurn, Severity: "info", EvidenceRefs: []string{ev.ID}})
	}
	d.snapshot.History = append(d.snapshot.History, ev)
	if len(d.snapshot.History) > 32 {
		d.snapshot.History = d.snapshot.History[len(d.snapshot.History)-32:]
	}
	filtered := signals[:0]
	for _, sig := range signals {
		shape := triggerShape(sig)
		if !urgent(sig.Type) && d.suppressed(shape, ev) {
			continue
		}
		d.rememberEmission(shape, ev)
		filtered = append(filtered, sig)
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Type < filtered[j].Type })
	return &Trigger{ID: triggerID(ev, filtered), Anchor: anchor, Signals: filtered, CreatedAt: ev.At}
}

func (d *Detector) rememberEmission(shape string, ev WorkerEvent) {
	d.snapshot.LastEmittedAt[shape] = ev.At
	d.snapshot.LastEmittedSeq[shape] = ev.Sequence
	for len(d.snapshot.LastEmittedAt) > maxDetectorCooldowns {
		oldest := ""
		for key, at := range d.snapshot.LastEmittedAt {
			if oldest == "" || at.Before(d.snapshot.LastEmittedAt[oldest]) || at.Equal(d.snapshot.LastEmittedAt[oldest]) && d.snapshot.LastEmittedSeq[key] < d.snapshot.LastEmittedSeq[oldest] {
				oldest = key
			}
		}
		delete(d.snapshot.LastEmittedAt, oldest)
		delete(d.snapshot.LastEmittedSeq, oldest)
	}
}

func boundWorkerEvent(ev WorkerEvent) WorkerEvent {
	ev.ID = truncateDetectorField(ev.ID, 256)
	ev.StepID = truncateDetectorField(ev.StepID, 256)
	ev.Tool = truncateDetectorField(ev.Tool, 512)
	ev.ArgsDigest = truncateDetectorField(ev.ArgsDigest, 512)
	ev.OutcomeDigest = truncateDetectorField(ev.OutcomeDigest, 512)
	ev.ErrorFingerprint = truncateDetectorField(ev.ErrorFingerprint, 512)
	ev.TreeDigest = truncateDetectorField(ev.TreeDigest, 512)
	ev.ChildID = truncateDetectorField(ev.ChildID, 512)
	ev.ChildStatus = truncateDetectorField(ev.ChildStatus, 256)
	ev.Boundary = truncateDetectorField(ev.Boundary, maxDetectorFieldRunes)
	ev.ChangedPaths = boundDetectorPaths(ev.ChangedPaths)
	ev.OutOfScopePaths = boundDetectorPaths(ev.OutOfScopePaths)
	return ev
}

func workerEventWithinBounds(ev WorkerEvent) bool {
	fields := []struct {
		value string
		limit int
	}{
		{ev.ID, 256}, {ev.StepID, 256}, {ev.Tool, 512},
		{ev.ArgsDigest, 512}, {ev.OutcomeDigest, 512},
		{ev.ErrorFingerprint, 512}, {ev.TreeDigest, 512},
		{ev.ChildID, 512}, {ev.ChildStatus, 256},
		{ev.Boundary, maxDetectorFieldRunes},
	}
	for _, field := range fields {
		if utf8.RuneCountInString(field.value) > field.limit {
			return false
		}
	}
	for _, paths := range [][]string{ev.ChangedPaths, ev.OutOfScopePaths} {
		if len(paths) > maxDetectorPaths {
			return false
		}
		for _, value := range paths {
			if utf8.RuneCountInString(value) > maxDetectorPathRunes {
				return false
			}
		}
	}
	return true
}

func boundDetectorPaths(paths []string) []string {
	if len(paths) > maxDetectorPaths {
		paths = paths[:maxDetectorPaths]
	}
	out := make([]string, 0, len(paths))
	for _, value := range paths {
		out = append(out, truncateDetectorField(value, maxDetectorPathRunes))
	}
	return out
}

func truncateDetectorField(value string, limit int) string {
	if limit < 1 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func (d *Detector) suppressed(shape string, ev WorkerEvent) bool {
	lastAt := d.snapshot.LastEmittedAt[shape]
	lastSeq, seen := d.snapshot.LastEmittedSeq[shape]
	if !seen {
		return false
	}
	return ev.Sequence <= lastSeq+2 || (!lastAt.IsZero() && ev.At.Sub(lastAt) < d.cooldown)
}

func (d *Detector) detect(prior []WorkerEvent, ev WorkerEvent) []TriggerSignal {
	var out []TriggerSignal
	ref := func(events ...WorkerEvent) []string {
		xs := make([]string, 0, len(events))
		for _, e := range events {
			xs = append(xs, e.ID)
		}
		return xs
	}
	switch ev.Kind {
	case WorkerToolOutcome:
		if !ev.Succeeded && ev.ErrorFingerprint != "" {
			matches := []WorkerEvent{}
			for i := len(prior) - 1; i >= 0 && len(matches) < 2; i-- {
				p := prior[i]
				if p.Kind == WorkerToolOutcome && !p.Succeeded && p.Tool == ev.Tool && p.ErrorFingerprint == ev.ErrorFingerprint {
					matches = append(matches, p)
				}
			}
			if len(matches) >= 1 {
				out = append(out, TriggerSignal{Type: TriggerRepeatedFailure, Severity: "warning", EvidenceRefs: ref(matches[0], ev), Attributes: map[string]string{"tool": ev.Tool, "fingerprint": ev.ErrorFingerprint}})
			}
			if len(matches) >= 2 && ev.ArgsDigest != "" && matches[0].ArgsDigest == ev.ArgsDigest && matches[1].ArgsDigest == ev.ArgsDigest {
				out = append(out, TriggerSignal{Type: TriggerRetryThrash, Severity: "high", EvidenceRefs: ref(matches[1], matches[0], ev), Attributes: map[string]string{"tool": ev.Tool, "args_digest": ev.ArgsDigest}})
			}
		}
	case WorkerVerification:
		if ev.VerificationPassed != nil && !*ev.VerificationPassed {
			if p, ok := lastEvent(prior, func(p WorkerEvent) bool { return p.Kind == WorkerVerification && p.VerificationPassed != nil }); ok && *p.VerificationPassed {
				out = append(out, TriggerSignal{Type: TriggerVerificationRegress, Severity: "high", EvidenceRefs: ref(p, ev)})
			}
		}
	case WorkerTreeChanged:
		if ev.TreeDigest != "" {
			trees := make([]WorkerEvent, 0, 2)
			for i := len(prior) - 1; i >= 0 && len(trees) < 2; i-- {
				if prior[i].Kind == WorkerTreeChanged {
					trees = append(trees, prior[i])
				}
			}
			if len(trees) == 2 && trees[1].TreeDigest == ev.TreeDigest && trees[0].TreeDigest != ev.TreeDigest {
				a, b := trees[1], trees[0]
				out = append(out, TriggerSignal{Type: TriggerEditRevert, Severity: "warning", EvidenceRefs: ref(a, b, ev)})
			}
		}
		if len(ev.OutOfScopePaths) > 0 {
			out = append(out, TriggerSignal{Type: TriggerScopeExpansion, Severity: "high", EvidenceRefs: ref(ev), Attributes: map[string]string{"paths": boundedJoin(ev.OutOfScopePaths, 256)}})
		} else if p, ok := lastEvent(prior, func(p WorkerEvent) bool { return p.Kind == WorkerTreeChanged }); ok {
			switch {
			case len(ev.ChangedPaths) > 12 || len(ev.ChangedPaths) >= 6 && len(ev.ChangedPaths) > len(p.ChangedPaths)*2:
				out = append(out, TriggerSignal{Type: TriggerScopeExpansion, Severity: "warning", EvidenceRefs: ref(p, ev), Attributes: map[string]string{"changed_path_count": fmt.Sprint(len(ev.ChangedPaths)), "paths": boundedJoin(ev.ChangedPaths, 256)}})
			case p.DiffBytes > 0 && ev.DiffBytes > 32_768 && ev.DiffBytes > p.DiffBytes*2:
				out = append(out, TriggerSignal{Type: TriggerScopeExpansion, Severity: "warning", EvidenceRefs: ref(p, ev), Attributes: map[string]string{"diff_bytes": fmt.Sprint(ev.DiffBytes)}})
			}
		}
	case WorkerTurnCompleted:
		turns := []WorkerEvent{ev}
		completedSteps := func(event WorkerEvent) int {
			if event.CompletedSteps == 0 && event.CriteriaCompleted != 0 {
				return event.CriteriaCompleted
			}
			return event.CompletedSteps
		}
		for i := len(prior) - 1; i >= 0 && len(turns) < 4; i-- {
			if prior[i].Kind == WorkerTurnCompleted {
				turns = append(turns, prior[i])
			}
		}
		if len(turns) == 4 {
			// EP-0062 defines this as a criterion-progress stall, not an
			// activity stall. New evidence and tree churn remain useful trigger
			// context, but only a step transition or completed-step progress
			// resets the four-turn window; otherwise busywork could suppress the
			// watchdog indefinitely.
			unchanged := true
			for _, p := range turns[1:] {
				if completedSteps(p) != completedSteps(ev) ||
					p.StepID != ev.StepID {
					unchanged = false
					break
				}
			}
			if unchanged {
				out = append(out, TriggerSignal{Type: TriggerNoProgress, Severity: "warning", EvidenceRefs: ref(turns[3], turns[2], turns[1], turns[0]), Attributes: map[string]string{
					"step": ev.StepID, "completed_steps": fmt.Sprint(completedSteps(ev)), "evidence_count": fmt.Sprint(ev.EvidenceCount), "tree_digest": ev.TreeDigest,
				}})
			}
		}
		if ev.TokenBudget > 0 && ev.TokenUsage*100 >= ev.TokenBudget*80 {
			out = append(out, TriggerSignal{Type: TriggerBudgetBurn, Severity: "warning", EvidenceRefs: ref(ev), Attributes: map[string]string{"used_percent": fmt.Sprint(ev.TokenUsage * 100 / ev.TokenBudget)}})
		}
	case WorkerChildLifecycle:
		if ev.ChildStatus == "failed" || ev.ChildStatus == "error" || ev.ChildStatus == "timeout" || ev.ChildStatus == "budget_exhausted" || ev.ChildStatus == "restart_exhausted" {
			out = append(out, TriggerSignal{Type: TriggerChildFailure, Severity: "high", EvidenceRefs: ref(ev), Attributes: map[string]string{"child": ev.ChildID, "status": ev.ChildStatus}})
		}
	case WorkerStepClaimed:
		out = append(out, TriggerSignal{Type: TriggerStepCompletion, Severity: "info", EvidenceRefs: ref(ev), Attributes: map[string]string{"step": ev.StepID}})
	case WorkerPivotRequested:
		out = append(out, TriggerSignal{Type: TriggerPivot, Severity: "high", EvidenceRefs: ref(ev)})
	case WorkerRiskBoundary:
		out = append(out, TriggerSignal{Type: TriggerRisk, Severity: "critical", EvidenceRefs: ref(ev), Attributes: map[string]string{"boundary": ev.Boundary}})
	case WorkerCompletionClaimed:
		out = append(out, TriggerSignal{Type: TriggerCompletion, Severity: "high", EvidenceRefs: ref(ev)})
	case WorkerCorrectionFollowup:
		out = append(out, TriggerSignal{Type: TriggerCorrectionFollowup, Severity: "high", EvidenceRefs: ref(ev)})
	}
	return out
}

func lastEvent(events []WorkerEvent, match func(WorkerEvent) bool) (WorkerEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if match(events[i]) {
			return events[i], true
		}
	}
	return WorkerEvent{}, false
}
func urgent(t TriggerType) bool {
	return t == TriggerPivot || t == TriggerRisk || t == TriggerCompletion || t == TriggerStepCompletion || t == TriggerCorrectionFollowup || t == TriggerStaleIntervention
}
func triggerShape(s TriggerSignal) string {
	keys := make([]string, 0, len(s.Attributes))
	for k := range s.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(string(s.Type))
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Attributes[k])
	}
	return b.String()
}
func triggerID(ev WorkerEvent, signals []TriggerSignal) string {
	var b strings.Builder
	b.WriteString(ev.ID)
	for _, s := range signals {
		b.WriteByte('|')
		b.WriteString(triggerShape(s))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "trigger_" + hex.EncodeToString(sum[:12])
}
func boundedJoin(xs []string, max int) string {
	joined := strings.Join(xs, ",")
	if len(joined) <= max {
		return joined
	}
	return joined[:max]
}
func cloneTimes(in map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneSeqs(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
