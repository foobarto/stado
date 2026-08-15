package application

// This file implements EP-0060's generic broker-owned session context
// projection. It intentionally exposes facts rather than guidance policy:
// signal attributes, mailbox payloads, artifact
// bodies, thresholds, labels, and recommendations never cross this boundary.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/sessioncontext"
)

const (
	contextSnapshotSchema = "stado.dev/session-context-facts/v1"
	maxContextSignals     = 128
	maxContextChildren    = 128
)

type ContextSignalFact struct {
	ID               string                    `json:"id"`
	Type             sessioncontext.SignalType `json:"type"`
	DetectorVersion  int                       `json:"detector_version"`
	Confidence       string                    `json:"confidence"`
	DetectedSequence uint64                    `json:"detected_sequence"`
	CreatedAt        time.Time                 `json:"created_at"`
	ExpiresAt        time.Time                 `json:"expires_at"`
}

type ContextChildStatus string

const (
	ContextChildActive    ContextChildStatus = "active"
	ContextChildAdmitted  ContextChildStatus = "admitted"
	ContextChildStarting  ContextChildStatus = "starting"
	ContextChildRunning   ContextChildStatus = "running"
	ContextChildIdle      ContextChildStatus = "idle"
	ContextChildCompleted ContextChildStatus = "completed"
	ContextChildFailed    ContextChildStatus = "failed"
	ContextChildCancelled ContextChildStatus = "cancelled"
	ContextChildDown      ContextChildStatus = "down"
	ContextChildArchived  ContextChildStatus = "archived"
	ContextChildDeleted   ContextChildStatus = "deleted"
)

type ContextChildFact struct {
	ID         string             `json:"id"`
	Status     ContextChildStatus `json:"status"`
	Generation uint64             `json:"generation,omitempty"`
}

type ContextSnapshot struct {
	Schema            string              `json:"schema"`
	AsOfSequence      uint64              `json:"as_of_sequence"`
	Digest            string              `json:"digest"`
	Signals           []ContextSignalFact `json:"signals"`
	SignalsTruncated  bool                `json:"signals_truncated"`
	Children          []ContextChildFact  `json:"children"`
	ChildrenTruncated bool                `json:"children_truncated"`
	UnreadMessages    int                 `json:"unread_messages"`
}

// contextSnapshotStore freezes all projections at one broker-WAL boundary.
// Its append method is deliberately unavailable: session:context:read can
// never become a second writer by passing this view into an existing service.
type contextSnapshotStore struct {
	records []wal.Record
	epoch   uint64
}

func (s *contextSnapshotStore) Append(wal.Transaction) (wal.AppendResult, error) {
	return wal.AppendResult{}, errors.New("read-only context snapshot")
}
func (s *contextSnapshotStore) Records() []wal.Record { return s.records }
func (s *contextSnapshotStore) Epoch() uint64         { return s.epoch }

// ReadContext projects bounded facts for the native-resolved logical subject
// attached to the opaque application binding. There is no request selector:
// callers cannot name another session, generation, path, or plugin identity.
func (s *Service) ReadContext(ctx context.Context, auth Authority) (ContextSnapshot, error) {
	if err := checkContext(ctx); err != nil {
		return ContextSnapshot{}, err
	}
	if err := s.validateAuthority(auth); err != nil {
		return ContextSnapshot{}, err
	}
	if invalidAuthorityPart(auth.Subject, s.limits.MaxIDBytes) {
		return ContextSnapshot{}, ErrScope
	}

	s.mu.Lock()
	records := append([]wal.Record(nil), s.wal.Records()...)
	epoch := uint64(0)
	if source, ok := s.wal.(interface{ Epoch() uint64 }); ok {
		epoch = source.Epoch()
	}
	s.mu.Unlock()
	snapshotStore := &contextSnapshotStore{records: records, epoch: epoch}
	result := ContextSnapshot{Schema: contextSnapshotSchema}
	if len(records) > 0 {
		result.AsOfSequence = records[len(records)-1].Sequence
	}

	signalProjection := sessioncontext.New(snapshotStore)
	signals, err := signalProjection.Signals(auth.Subject, false)
	if err != nil {
		return ContextSnapshot{}, err
	}
	detectedAt := make(map[string]uint64, len(signals))
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != "session_context" || event.Type != "signal.detected" || event.Session != auth.Subject {
				continue
			}
			var signal sessioncontext.Signal
			if err := json.Unmarshal(event.Data, &signal); err != nil {
				return ContextSnapshot{}, err
			}
			detectedAt[signal.ID] = record.Sequence
		}
	}
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].CreatedAt.Equal(signals[j].CreatedAt) {
			return signals[i].ID < signals[j].ID
		}
		return signals[i].CreatedAt.Before(signals[j].CreatedAt)
	})
	if len(signals) > maxContextSignals {
		result.SignalsTruncated = true
		signals = signals[len(signals)-maxContextSignals:]
	}
	for _, signal := range signals {
		result.Signals = append(result.Signals, ContextSignalFact{
			ID: signal.ID, Type: signal.Type, DetectorVersion: signal.DetectorVersion,
			Confidence: signal.Confidence, DetectedSequence: detectedAt[signal.ID],
			CreatedAt: signal.CreatedAt, ExpiresAt: signal.ExpiresAt,
		})
	}

	children := make(map[string]ContextChildFact)
	state, err := signalProjection.State(auth.Subject)
	if err != nil {
		return ContextSnapshot{}, err
	}
	for _, child := range state.ActiveChildren {
		children[child] = ContextChildFact{ID: child, Status: ContextChildActive}
	}
	admissions, err := retained.New(snapshotStore).List()
	if err != nil {
		return ContextSnapshot{}, err
	}
	for _, admission := range admissions {
		if admission.ParentSessionID == auth.Subject {
			status, ok := retainedContextChildStatus(admission.Status)
			if !ok {
				return ContextSnapshot{}, errors.New("retained child has an unknown lifecycle status")
			}
			children[admission.ChildSessionID] = ContextChildFact{
				ID: admission.ChildSessionID, Status: status, Generation: admission.Generation,
			}
		}
	}
	for _, child := range children {
		result.Children = append(result.Children, child)
	}
	sort.Slice(result.Children, func(i, j int) bool { return result.Children[i].ID < result.Children[j].ID })
	if len(result.Children) > maxContextChildren {
		result.ChildrenTruncated = true
		result.Children = result.Children[:maxContextChildren]
	}
	result.UnreadMessages, err = mailbox.PendingCount(snapshotStore, auth.Subject)
	if err != nil {
		return ContextSnapshot{}, err
	}

	digestInput := result
	digestInput.Digest = ""
	encoded, err := json.Marshal(digestInput)
	if err != nil {
		return ContextSnapshot{}, err
	}
	digest := sha256.Sum256(encoded)
	result.Digest = hex.EncodeToString(digest[:])
	return result, nil
}

func retainedContextChildStatus(status retained.Status) (ContextChildStatus, bool) {
	switch status {
	case retained.StatusAdmitted:
		return ContextChildAdmitted, true
	case retained.StatusStarting:
		return ContextChildStarting, true
	case retained.StatusRunning:
		return ContextChildRunning, true
	case retained.StatusIdle:
		return ContextChildIdle, true
	case retained.StatusCompleted:
		return ContextChildCompleted, true
	case retained.StatusFailed:
		return ContextChildFailed, true
	case retained.StatusCancelled:
		return ContextChildCancelled, true
	case retained.StatusDown:
		return ContextChildDown, true
	case retained.StatusArchived:
		return ContextChildArchived, true
	case retained.StatusDeleted:
		return ContextChildDeleted, true
	default:
		return "", false
	}
}
