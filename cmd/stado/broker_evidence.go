package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

const (
	maxSessionEvidenceScanBytes = 8 << 20
	maxSessionEvidenceLineage   = 100
)

type brokerSessionEvidenceSource struct{ cfg *config.Config }

func (s brokerSessionEvidenceSource) AuthorizedSessions(ctx context.Context, scope broker.EvidenceSessionScope) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.cfg == nil || stadogit.ValidateSessionID(scope.RootSessionID) != nil || scope.CanonicalRepo == "" {
		return nil, errors.New("invalid authenticated session evidence scope")
	}
	rootPath, err := worktreePathForID(s.cfg.WorktreeDir(), scope.RootSessionID)
	if err != nil {
		return nil, err
	}
	userRepo := runtime.ReadUserRepoPin(rootPath)
	if userRepo == "" {
		return nil, errors.New("authorized session repository pin is unavailable")
	}
	repoID, err := stadogit.RepoID(userRepo)
	if err != nil || repoID != scope.CanonicalRepo {
		return nil, errors.New("session evidence repository scope mismatch")
	}
	sidecarPath := s.cfg.SidecarPath(userRepo, repoID)
	if _, err := os.Stat(sidecarPath); err != nil {
		return nil, errors.New("authorized session sidecar is unavailable")
	}
	sidecar, err := stadogit.OpenOrInitSidecar(sidecarPath, userRepo)
	if err != nil {
		return nil, errors.New("authorized session evidence is unavailable")
	}
	session, err := stadogit.OpenSession(sidecar, s.cfg.WorktreeDir(), scope.RootSessionID)
	if err != nil {
		return nil, errors.New("authorized session evidence is unavailable")
	}
	ancestors, err := runtime.SessionAncestors(session.Sidecar, s.cfg.WorktreeDir(), scope.RootSessionID)
	if err != nil {
		return nil, err
	}
	return boundedSessionEvidenceLineage(scope.RootSessionID, ancestors), nil
}

func boundedSessionEvidenceLineage(root string, ancestors []string) []string {
	lineage := append([]string{root}, ancestors...)
	if len(lineage) > maxSessionEvidenceLineage {
		lineage = lineage[:maxSessionEvidenceLineage]
	}
	return lineage
}

func (s brokerSessionEvidenceSource) Catalog(ctx context.Context, scope broker.EvidenceSessionScope, limit int) ([]broker.EvidenceItem, error) {
	ids, err := s.AuthorizedSessions(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	items := make([]broker.EvidenceItem, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		records, err := s.records(id)
		if err != nil || len(records) == 0 {
			continue
		}
		last := records[len(records)-1]
		items = append(items, broker.EvidenceItem{Ref: sessionRecordRef(id, last), Summary: fmt.Sprintf("session %s; immutable record %d of %d: %s", id, len(records), len(records), boundedSessionEvidence(string(last.body), 320))})
	}
	return items, nil
}

func (s brokerSessionEvidenceSource) Search(ctx context.Context, scope broker.EvidenceSessionScope, query string, limit int) ([]broker.EvidenceItem, error) {
	ids, err := s.AuthorizedSessions(ctx, scope)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil, errors.New("session evidence query required")
	}
	var items []broker.EvidenceItem
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		records, err := s.records(id)
		if err != nil {
			continue
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.Contains(strings.ToLower(string(record.body)), needle) {
				continue
			}
			items = append(items, broker.EvidenceItem{Ref: sessionRecordRef(id, record), Summary: boundedSessionEvidence(string(record.body), 512)})
			if len(items) >= limit {
				return items, nil
			}
		}
	}
	return items, nil
}

func (s brokerSessionEvidenceSource) Open(ctx context.Context, scope broker.EvidenceSessionScope, ref broker.EvidenceRef, maxBytes int) (broker.EvidenceOpened, error) {
	if err := ctx.Err(); err != nil {
		return broker.EvidenceOpened{}, err
	}
	ids, err := s.AuthorizedSessions(ctx, scope)
	if err != nil {
		return broker.EvidenceOpened{}, err
	}
	authorized := false
	for _, id := range ids {
		if id == ref.ID {
			authorized = true
			break
		}
	}
	if !authorized || ref.Corpus != "session" || ref.Kind != "conversation-record" {
		return broker.EvidenceOpened{}, errors.New("session evidence not found")
	}
	start, end, err := parseSessionEvidenceLocator(ref.Locator)
	if err != nil || end-start > maxBytes {
		return broker.EvidenceOpened{}, errors.New("invalid session evidence range")
	}
	raw, err := runtime.RawConversationLog(s.sessionPath(ref.ID))
	if err != nil || start < 0 || end > len(raw) || start >= end {
		return broker.EvidenceOpened{}, errors.New("session evidence range is unavailable")
	}
	body := append([]byte(nil), raw[start:end]...)
	actual := broker.EvidenceRef{Corpus: "session", Kind: "conversation-record", ID: ref.ID, Locator: ref.Locator, Digest: digestEvidence(body)}
	if actual != ref {
		return broker.EvidenceOpened{}, errors.New("session evidence range changed or was fabricated")
	}
	return broker.EvidenceOpened{Ref: actual, Body: string(body)}, nil
}

type sessionEvidenceRecord struct {
	start int
	end   int
	body  []byte
}

func (s brokerSessionEvidenceSource) records(id string) ([]sessionEvidenceRecord, error) {
	if stadogit.ValidateSessionID(id) != nil {
		return nil, errors.New("invalid session id")
	}
	raw, err := runtime.RawConversationLog(s.sessionPath(id))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSessionEvidenceScanBytes {
		return nil, fmt.Errorf("session evidence log exceeds %d-byte scan ceiling", maxSessionEvidenceScanBytes)
	}
	var records []sessionEvidenceRecord
	start := 0
	for start < len(raw) {
		rel := bytes.IndexByte(raw[start:], '\n')
		if rel < 0 {
			break // incomplete append-only tail is never evidence
		}
		end := start + rel
		line := raw[start:end]
		var valid json.RawMessage
		if len(line) != 0 && json.Unmarshal(line, &valid) == nil {
			records = append(records, sessionEvidenceRecord{start: start, end: end, body: append([]byte(nil), line...)})
		}
		start = end + 1
	}
	return records, nil
}

func (s brokerSessionEvidenceSource) sessionPath(id string) string {
	path, _ := worktreePathForID(s.cfg.WorktreeDir(), id)
	return path
}

func sessionRecordRef(id string, record sessionEvidenceRecord) broker.EvidenceRef {
	return broker.EvidenceRef{Corpus: "session", Kind: "conversation-record", ID: id,
		Locator: fmt.Sprintf("conversation.jsonl:bytes:%d-%d", record.start, record.end), Digest: digestEvidence(record.body)}
}

func parseSessionEvidenceLocator(locator string) (int, int, error) {
	const prefix = "conversation.jsonl:bytes:"
	if !strings.HasPrefix(locator, prefix) {
		return 0, 0, errors.New("invalid locator")
	}
	parts := strings.Split(strings.TrimPrefix(locator, prefix), "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid locator")
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || start < 0 || end <= start {
		return 0, 0, errors.New("invalid locator")
	}
	return start, end, nil
}

func boundedSessionEvidence(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func digestEvidence(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
