package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/memory"
)

const LegacyConverterVersion = 1

type LegacyMigration struct {
	Items       []memory.Item
	RawLog      []byte
	ArchivePath string
	Principal   string
	Actor       string
	// ValidateSessionAnchor must resolve legacy session ancestry from trusted
	// session metadata. Unresolved items are quarantined rather than broadened.
	ValidateSessionAnchor func(string) bool
}

type MigrationResult struct {
	Migrated      int      `json:"migrated"`
	Quarantined   []string `json:"quarantined,omitempty"`
	ArchiveDigest string   `json:"archive_digest"`
}

// MigrateLegacy preserves the legacy bytes first, then commits the converter
// manifest and all convertible folded artifacts in one canonical transaction.
// Re-running with the same archive digest is idempotent.
func (s *Service) MigrateLegacy(ctx context.Context, req LegacyMigration) (MigrationResult, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Principal == "" || req.Actor == "" || req.ArchivePath == "" {
		return MigrationResult{}, errors.New("legacy migration requires principal, actor, and archive path")
	}
	sum := sha256.Sum256(req.RawLog)
	digest := hex.EncodeToString(sum[:])
	if prior, ok := completedMigration(s.wal.Records(), digest); ok {
		return prior, nil
	}
	if err := preserveArchive(req.ArchivePath, req.RawLog, digest); err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{ArchiveDigest: digest}
	events := make([]wal.Event, 0, len(req.Items)+1)
	manifest, _ := json.Marshal(struct {
		Digest    string `json:"legacy_digest"`
		Bytes     int    `json:"legacy_bytes"`
		Converter int    `json:"converter_version"`
	}{digest, len(req.RawLog), LegacyConverterVersion})
	events = append(events, wal.Event{Store: artifactStore, Type: "migration.genesis", Data: manifest})
	for _, kind := range []Kind{KindMemory, KindLesson} {
		desc, ok := s.kinds.Lookup(kind)
		if !ok {
			return MigrationResult{}, fmt.Errorf("legacy migration kind %q is not registered", kind)
		}
		events = append(events, s.kindRegistrationEvents(desc)...)
	}
	seen := map[string]bool{}
	for _, old := range req.Items {
		if old.ID == "" || seen[old.ID] {
			result.Quarantined = append(result.Quarantined, old.ID)
			continue
		}
		seen[old.ID] = true
		a, err := s.convertLegacy(old, req.Principal, req.ValidateSessionAnchor)
		if err != nil {
			result.Quarantined = append(result.Quarantined, old.ID)
			continue
		}
		if _, exists, err := s.showLocked(a.ID); err != nil {
			return MigrationResult{}, err
		} else if exists {
			result.Quarantined = append(result.Quarantined, old.ID)
			continue
		}
		b, _ := json.Marshal(createEvent{Artifact: a})
		events = append(events, wal.Event{Store: artifactStore, Type: "artifact.create", Data: b})
		result.Migrated++
	}
	resultBytes, _ := json.Marshal(result)
	events = append(events, wal.Event{Store: artifactStore, Type: "migration.completed", Data: resultBytes})
	_, err := s.wal.Append(wal.Transaction{ID: mintID(), IdempotencyKey: "legacy-migration:" + digest, Principal: req.Principal, Actor: req.Actor, Events: events})
	return result, err
}

func completedMigration(records []wal.Record, digest string) (MigrationResult, bool) {
	for _, rec := range records {
		if rec.Transaction.IdempotencyKey != "legacy-migration:"+digest {
			continue
		}
		for _, ev := range rec.Transaction.Events {
			if ev.Store == artifactStore && ev.Type == "migration.completed" {
				var result MigrationResult
				if json.Unmarshal(ev.Data, &result) == nil && result.ArchiveDigest == digest {
					return result, true
				}
			}
		}
	}
	return MigrationResult{}, false
}

func (s *Service) convertLegacy(old memory.Item, principal string, validate func(string) bool) (Artifact, error) {
	kind := KindMemory
	if memory.IsLesson(old) {
		kind = KindLesson
	}
	learning := LearningData{Summary: old.Summary, Content: old.Body, Trigger: old.Trigger}
	if kind == KindLesson {
		learning.Content = old.Lesson
		learning.ExpectedOutcome = old.Rationale
	}
	a := LearningArtifact(kind, Scope(old.Scope), ScopeBinding{}, learning)
	a.ID, a.Version = old.ID, 1
	a.Tags = append([]string(nil), old.Tags...)
	a.Sensitivity = old.Sensitivity
	a.CreatedAt, a.UpdatedAt, a.ExpiresAt = old.CreatedAt, old.UpdatedAt, old.ExpiresAt
	a.Supersedes, a.LegacyID = append([]string(nil), old.Supersedes...), old.ID
	a.Binding.Principal = principal
	switch a.Scope {
	case ScopeGlobal:
	case ScopeRepo:
		a.Binding.CanonicalRepoID = strings.TrimSpace(old.RepoID)
	case ScopeSession:
		anchor := strings.TrimSpace(old.SessionID)
		if anchor == "" || validate == nil || !validate(anchor) {
			return Artifact{}, errors.New("unresolved session anchor")
		}
		a.Binding.AnchorSessionID = anchor
	default:
		return Artifact{}, errors.New("invalid legacy scope")
	}
	for _, c := range old.Evidence.Commits {
		a.EvidenceRefs = append(a.EvidenceRefs, "commit:"+c)
	}
	for _, f := range old.Evidence.Files {
		a.EvidenceRefs = append(a.EvidenceRefs, "file:"+f)
	}
	for _, test := range old.Evidence.Tests {
		a.EvidenceRefs = append(a.EvidenceRefs, "test:"+test)
	}
	if old.Evidence.SessionID != "" {
		a.EvidenceRefs = append(a.EvidenceRefs, "session:"+old.Evidence.SessionID)
	}
	if _, err := s.prepare(&a, principal); err != nil {
		return Artifact{}, err
	}
	switch strings.ToLower(old.Confidence) {
	case "approved", "active":
		if kind == KindLesson {
			a.Authority = AuthorityLegacyActive
		} else {
			a.Authority = AuthorityActive
		}
	case "candidate", "":
		a.Authority = AuthorityCandidate
	case "rejected":
		a.Authority = AuthorityRejected
	case "superseded":
		a.Authority = AuthoritySuperseded
	case "deleted":
		a.Authority = AuthorityDeleted
	default:
		return Artifact{}, fmt.Errorf("invalid legacy confidence %q", old.Confidence)
	}
	return a, nil
}

func preserveArchive(path string, data []byte, digest string) error {
	if existing, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(existing)
		if hex.EncodeToString(sum[:]) != digest {
			return errors.New("legacy archive digest mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
