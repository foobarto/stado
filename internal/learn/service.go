// Package learn implements evidence-backed trajectory review. Reviewer output
// is always an untrusted artifact candidate; it cannot activate itself.
package learn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/sessioncontext"
)

const storeName = "learn"

type Candidate struct {
	Summary         string   `json:"summary"`
	Lesson          string   `json:"lesson"`
	Trigger         string   `json:"trigger"`
	ExpectedOutcome string   `json:"expected_outcome,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Groups          []string `json:"groups,omitempty"`
	EvidenceRefs    []string `json:"evidence_refs"`
}

type ReviewInput struct {
	JobID         string                  `json:"job_id"`
	SessionID     string                  `json:"session_id"`
	Focus         string                  `json:"focus,omitempty"`
	AsOf          uint64                  `json:"as_of_sequence"`
	Signals       []sessioncontext.Signal `json:"signals"`
	MaxCandidates int                     `json:"max_candidates"`
}

type Reviewer interface {
	Review(context.Context, ReviewInput) ([]Candidate, error)
}

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
)

type Job struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	Focus       string    `json:"focus,omitempty"`
	AsOf        uint64    `json:"as_of_sequence"`
	Status      JobStatus `json:"status"`
	ArtifactIDs []string  `json:"artifact_ids,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}
type signalSource interface {
	SignalsAt(string, bool, uint64) ([]sessioncontext.Signal, error)
}

type Service struct {
	wal       appender
	signals   signalSource
	artifacts *artifacts.Service
	reviewer  Reviewer
	now       func() time.Time
}

func New(store appender, signals signalSource, artifactService *artifacts.Service, reviewer Reviewer) *Service {
	return &Service{wal: store, signals: signals, artifacts: artifactService, reviewer: reviewer, now: time.Now}
}

type RunRequest struct {
	SessionID, Focus, Principal, Actor, IdempotencyKey string
	Scope                                              artifacts.Scope
	Binding                                            artifacts.ScopeBinding
	MaxCandidates                                      int
}

func (s *Service) Run(ctx context.Context, req RunRequest) (Job, error) {
	if req.SessionID == "" || req.Principal == "" || req.Actor == "" || req.IdempotencyKey == "" {
		return Job{}, errors.New("learn run requires session, principal, actor, and idempotency key")
	}
	if s.reviewer == nil {
		return Job{}, errors.New("learn reviewer unavailable")
	}
	if req.MaxCandidates <= 0 || req.MaxCandidates > 8 {
		req.MaxCandidates = 5
	}
	records := s.wal.Records()
	asOf := uint64(0)
	if len(records) > 0 {
		asOf = records[len(records)-1].Sequence
	}
	job := Job{ID: mint("learn_"), SessionID: req.SessionID, Focus: req.Focus, AsOf: asOf, Status: JobQueued, CreatedAt: s.now().UTC(), UpdatedAt: s.now().UTC()}
	if err := s.appendJob(req, job, "job.queued", req.IdempotencyKey+":queued"); err != nil {
		return Job{}, err
	}
	job.Status = JobRunning
	job.UpdatedAt = s.now().UTC()
	if err := s.appendJob(req, job, "job.running", req.IdempotencyKey+":running"); err != nil {
		return Job{}, err
	}
	signals, err := s.signals.SignalsAt(req.SessionID, false, asOf)
	if err != nil {
		return s.fail(req, job, err)
	}
	// Capture only signals whose canonical creation was visible at the boundary.
	input := ReviewInput{JobID: job.ID, SessionID: req.SessionID, Focus: req.Focus, AsOf: asOf, Signals: signals, MaxCandidates: req.MaxCandidates}
	candidates, err := s.reviewer.Review(ctx, input)
	if err != nil {
		return s.fail(req, job, err)
	}
	if len(candidates) > req.MaxCandidates {
		return s.fail(req, job, errors.New("reviewer exceeded candidate cap"))
	}
	allowed := map[string]bool{}
	for _, sig := range signals {
		allowed["signal:"+sig.ID] = true
		for _, ref := range sig.OriginRefs {
			allowed[ref] = true
		}
	}
	for n, c := range candidates {
		if err := validateCandidate(c, allowed); err != nil {
			return s.fail(req, job, fmt.Errorf("candidate %d: %w", n, err))
		}
		proposal := artifacts.LearningArtifact(artifacts.KindLesson, req.Scope, req.Binding, artifacts.LearningData{
			Summary: c.Summary, Content: c.Lesson, Trigger: c.Trigger,
			ExpectedOutcome: c.ExpectedOutcome,
		})
		proposal.Tags, proposal.Groups = c.Tags, c.Groups
		proposal.EvidenceRefs = c.EvidenceRefs
		proposal.Provenance = artifacts.Provenance{
			Origins:   []string{"untrusted:reviewer"},
			CreatedBy: "learn-reviewer",
			Refs:      []string{"learn-job:" + job.ID},
		}
		item, err := s.artifacts.Create(ctx, proposal, req.Principal, "learn-reviewer", req.IdempotencyKey+fmt.Sprintf(":candidate:%d", n))
		if err != nil {
			return s.fail(req, job, err)
		}
		job.ArtifactIDs = append(job.ArtifactIDs, item.ID)
	}
	job.Status = JobCompleted
	job.UpdatedAt = s.now().UTC()
	if err := s.appendJob(req, job, "job.completed", req.IdempotencyKey+":completed"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func validateCandidate(c Candidate, allowed map[string]bool) error {
	if strings.TrimSpace(c.Summary) == "" || strings.TrimSpace(c.Lesson) == "" || strings.TrimSpace(c.Trigger) == "" {
		return errors.New("summary, lesson, and trigger required")
	}
	if len(c.EvidenceRefs) == 0 {
		return errors.New("evidence required")
	}
	for _, ref := range c.EvidenceRefs {
		if !allowed[ref] {
			return fmt.Errorf("evidence %q was not in captured signal corpus", ref)
		}
	}
	return nil
}
func (s *Service) fail(req RunRequest, job Job, cause error) (Job, error) {
	job.Status = JobFailed
	job.Error = cause.Error()
	job.UpdatedAt = s.now().UTC()
	_ = s.appendJob(req, job, "job.failed", req.IdempotencyKey+":failed")
	return job, cause
}
func (s *Service) appendJob(req RunRequest, job Job, typ, idem string) error {
	b, _ := json.Marshal(job)
	_, err := s.wal.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: req.Principal, Actor: req.Actor, Events: []wal.Event{{Store: storeName, Type: typ, Session: req.SessionID, Data: b}}})
	return err
}
func (s *Service) Jobs(session string) ([]Job, error) {
	heads := map[string]Job{}
	order := []string{}
	for _, rec := range s.wal.Records() {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName || ev.Session != session || !strings.HasPrefix(ev.Type, "job.") {
				continue
			}
			var j Job
			if err := json.Unmarshal(ev.Data, &j); err != nil {
				return nil, err
			}
			if _, ok := heads[j.ID]; !ok {
				order = append(order, j.ID)
			}
			heads[j.ID] = j
		}
	}
	out := make([]Job, 0, len(order))
	for _, id := range order {
		out = append(out, heads[id])
	}
	return out, nil
}
func mint(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
