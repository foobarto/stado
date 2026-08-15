package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
)

const (
	maxEvidenceRows        = 100
	maxEvidenceCalls       = 40
	maxEvidenceOpens       = 20
	maxEvidenceReadBytes   = 256 << 10
	maxEvidenceOpenBytes   = 32 << 10
	maxEvidenceResultBytes = 16 << 10
	maxEvidenceExcerpt     = 1 << 10
)

const evidenceSchema = "stado.dev/evidence-read/v1"

type EvidenceRef struct {
	Corpus  string `json:"corpus"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version uint64 `json:"version,omitempty"`
	Locator string `json:"locator"`
	Digest  string `json:"digest"`
}

type EvidenceItem struct {
	Ref     EvidenceRef `json:"ref"`
	Summary string      `json:"summary"`
	Tags    []string    `json:"tags,omitempty"`
	Groups  []string    `json:"groups,omitempty"`
}

type EvidenceOpened struct {
	Ref       EvidenceRef `json:"ref"`
	Body      string      `json:"body"`
	ReceiptID string      `json:"receipt_id"`
}

// EvidenceSessionScope is derived by the broker from the authenticated child
// controller and its durable logical parent. RootSessionID is a git session
// subject, never a broker ID or guest selector.
type EvidenceSessionScope struct {
	Principal     string
	CanonicalRepo string
	RepoRoot      string
	RootSessionID string
}

// SessionEvidenceSource supplies bounded immutable transcript ranges. The
// broker chooses Scope; implementations must re-resolve ancestry on every call
// and reject refs outside that set before reading bytes.
type SessionEvidenceSource interface {
	AuthorizedSessions(context.Context, EvidenceSessionScope) ([]string, error)
	Catalog(context.Context, EvidenceSessionScope, int) ([]EvidenceItem, error)
	Search(context.Context, EvidenceSessionScope, string, int) ([]EvidenceItem, error)
	Open(context.Context, EvidenceSessionScope, EvidenceRef, int) (EvidenceOpened, error)
}

type evidenceReceipt struct {
	Schema       string      `json:"schema"`
	ReceiptID    string      `json:"receipt_id"`
	SessionID    string      `json:"session_id"`
	Generation   uint64      `json:"generation"`
	ParentID     string      `json:"parent_id"`
	Plugin       string      `json:"plugin"`
	Ref          EvidenceRef `json:"ref"`
	OpenedBody   string      `json:"opened_body"`
	OpenedDigest string      `json:"opened_digest"`
}

type evidenceUsageEvent struct {
	ScopeKey  string `json:"scope_key"`
	Calls     int    `json:"calls"`
	Opens     int    `json:"opens"`
	ReadBytes int    `json:"read_bytes"`
}

type evidenceUsage struct{ Calls, Opens, ReadBytes int }

func (s *Service) ConfigureSessionEvidenceSource(source SessionEvidenceSource) error {
	if source == nil {
		return errors.New("broker: session evidence source required")
	}
	if s.artifacts == nil || s.artifacts.store == nil {
		return errors.New("broker: evidence store unavailable")
	}
	if s.artifacts.evidenceSessions != nil {
		return errors.New("broker: session evidence source already configured")
	}
	s.artifacts.evidenceSessions = source
	return nil
}

func (s *Service) bindEvidence(ctx context.Context, params EvidenceBindParams) (ArtifactBindResult, error) {
	ordinary := ArtifactBindParams(params)
	return s.bindPlugin(ctx, ordinary.SessionID, ordinary.ControllerToken, ordinary.Identity, ordinary.Manifest, ordinary.ToolName, false)
}

func (s *Service) evidenceCall(ctx context.Context, params EvidenceCallParams) (json.RawMessage, error) {
	binding, err := s.artifactBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	if len(params.Payload) == 0 || len(params.Payload) > maxArtifactRPCBytes {
		return nil, errors.New("evidence payload is empty or oversized")
	}
	switch params.Operation {
	case "catalog", "search", "open":
		return s.evidenceRead(ctx, binding, params.Operation, params.Payload)
	case "validate":
		if !binding.hasCapability("evidence:validate") {
			return nil, errors.New("evidence:validate capability required")
		}
		return s.validateEvidenceResult(binding, params.Payload)
	default:
		return nil, errors.New("unknown evidence operation")
	}
}

func (s *Service) evidenceRead(ctx context.Context, binding artifactBinding, operation string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Corpus string      `json:"corpus"`
		Query  string      `json:"query,omitempty"`
		Limit  int         `json:"limit,omitempty"`
		Ref    EvidenceRef `json:"ref,omitempty"`
	}
	if err := strictUnmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Corpus != "artifact" && request.Corpus != "session" {
		return nil, errors.New("evidence corpus must be artifact or session")
	}
	if !binding.hasCapability("evidence:" + operation + ":" + request.Corpus) {
		return nil, fmt.Errorf("evidence:%s:%s capability required", operation, request.Corpus)
	}
	if request.Limit <= 0 {
		request.Limit = 20
	}
	if request.Limit > maxEvidenceRows {
		return nil, fmt.Errorf("evidence limit exceeds %d", maxEvidenceRows)
	}
	if operation == "search" && (strings.TrimSpace(request.Query) == "" || len(request.Query) > 4096) {
		return nil, errors.New("bounded evidence query required")
	}

	scope, err := s.evidenceScope(binding.sessionID)
	if err != nil {
		return nil, err
	}
	var response any
	openDelta := 0
	switch request.Corpus {
	case "artifact":
		response, err = s.artifactEvidence(ctx, operation, binding, scope, request.Query, request.Limit, request.Ref)
	case "session":
		response, err = s.sessionEvidence(ctx, operation, scope, request.Query, request.Limit, request.Ref)
	}
	if err != nil {
		return nil, err
	}
	if operation == "open" {
		opened, ok := response.(EvidenceOpened)
		if !ok || len(opened.Body) > maxEvidenceOpenBytes {
			return nil, errors.New("invalid evidence open response")
		}
		opened.ReceiptID = evidenceReceiptID(binding, opened)
		response = opened
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if operation == "open" {
		openDelta = 1
		if len(encoded) > maxEvidenceOpenBytes+(4<<10) {
			return nil, errors.New("evidence open response exceeds host limit")
		}
	}
	if err := s.commitEvidenceUsageAndReceipt(binding, operation, raw, encoded, openDelta, response); err != nil {
		return nil, err
	}
	return encoded, nil
}

func (s *Service) evidenceScope(sessionID string) (EvidenceSessionScope, error) {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return EvidenceSessionScope{}, ErrSessionNotFound
	}
	if state.terminated {
		return EvidenceSessionScope{}, ErrSessionTerminated
	}
	source := state
	if !source.scope.durable && source.parentID != "" {
		source = s.sessions[source.parentID]
	}
	if source == nil || !source.scope.durable || source.scope.subject == "" {
		return EvidenceSessionScope{}, errors.New("evidence requires a durable logical session scope")
	}
	return EvidenceSessionScope{
		Principal: state.principal, CanonicalRepo: state.repoID,
		RepoRoot: source.handle.CWD, RootSessionID: source.scope.subject,
	}, nil
}

func (s *Service) artifactEvidence(ctx context.Context, operation string, binding artifactBinding, scope EvidenceSessionScope, query string, limit int, ref EvidenceRef) (any, error) {
	if s.artifacts == nil || s.artifacts.service == nil {
		return nil, errors.New("artifact evidence unavailable")
	}
	var ancestors []string
	if s.artifacts.evidenceSessions != nil {
		ids, err := s.artifacts.evidenceSessions.AuthorizedSessions(ctx, scope)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if id != scope.RootSessionID {
				ancestors = append(ancestors, id)
			}
		}
	}
	qctx := artifacts.QueryContext{Principal: binding.principal, CanonicalRepoID: binding.repoID, SessionID: scope.RootSessionID, AncestorSessionIDs: ancestors}
	if operation == "open" {
		if ref.Corpus != "artifact" || ref.ID == "" || ref.Version == 0 {
			return nil, errors.New("exact artifact evidence ref required")
		}
		item, ok, err := s.artifacts.service.Visible(ref.ID, ref.Version, qctx)
		if err != nil || !ok || item.Authority != artifacts.AuthorityActive || item.Sensitivity == "secret" {
			return nil, errors.New("artifact evidence not found")
		}
		body, _ := json.Marshal(item)
		actual := artifactEvidenceRef(item, body)
		if ref != actual {
			return nil, errors.New("artifact evidence ref changed or was fabricated")
		}
		if len(body) > maxEvidenceOpenBytes {
			return nil, errors.New("artifact evidence body exceeds open limit")
		}
		return EvidenceOpened{Ref: actual, Body: string(body)}, nil
	}
	items, err := s.artifacts.service.Query(artifacts.Query{Context: qctx, ActiveOnly: true, ExcludeSecret: true, MaxItems: maxEvidenceRows})
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		item  artifacts.Artifact
		score int
	}
	var selected []scored
	for _, item := range items {
		projection, projectErr := s.artifacts.service.Project(item)
		if projectErr != nil {
			return nil, projectErr
		}
		score := 1
		if operation == "search" {
			score = 0
			haystack := strings.ToLower(projection.Title + " " + projection.Text + " " + projection.Trigger + " " + strings.Join(item.Tags, " ") + " " + strings.Join(item.Groups, " "))
			for _, term := range terms {
				if strings.Contains(haystack, term) {
					score++
				}
			}
		}
		if score > 0 {
			selected = append(selected, scored{item: item, score: score})
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].score != selected[j].score {
			return selected[i].score > selected[j].score
		}
		return selected[i].item.ID < selected[j].item.ID
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}
	out := make([]EvidenceItem, 0, len(selected))
	for _, selected := range selected {
		body, _ := json.Marshal(selected.item)
		projection, projectErr := s.artifacts.service.Project(selected.item)
		if projectErr != nil {
			return nil, projectErr
		}
		out = append(out, EvidenceItem{Ref: artifactEvidenceRef(selected.item, body), Summary: boundedEvidenceText(projection.Title, 512), Tags: selected.item.Tags, Groups: selected.item.Groups})
	}
	return map[string]any{"items": out}, nil
}

func artifactEvidenceRef(item artifacts.Artifact, body []byte) EvidenceRef {
	return EvidenceRef{Corpus: "artifact", Kind: string(item.Kind), ID: item.ID, Version: item.Version,
		Locator: fmt.Sprintf("artifact:%s@%d", item.ID, item.Version), Digest: evidenceDigest(body)}
}

func (s *Service) sessionEvidence(ctx context.Context, operation string, scope EvidenceSessionScope, query string, limit int, ref EvidenceRef) (any, error) {
	if s.artifacts == nil || s.artifacts.evidenceSessions == nil {
		return nil, errors.New("session evidence unavailable")
	}
	switch operation {
	case "catalog":
		items, err := s.artifacts.evidenceSessions.Catalog(ctx, scope, limit)
		return map[string]any{"items": items}, err
	case "search":
		items, err := s.artifacts.evidenceSessions.Search(ctx, scope, query, limit)
		return map[string]any{"items": items}, err
	case "open":
		if ref.Corpus != "session" || ref.ID == "" || ref.Locator == "" || ref.Digest == "" {
			return nil, errors.New("exact session evidence ref required")
		}
		return s.artifacts.evidenceSessions.Open(ctx, scope, ref, maxEvidenceOpenBytes)
	default:
		return nil, errors.New("unknown session evidence operation")
	}
}

func (s *Service) commitEvidenceUsageAndReceipt(binding artifactBinding, operation string, request, response []byte, opens int, value any) error {
	state := s.artifacts
	if state == nil || state.store == nil {
		return errors.New("evidence WAL unavailable")
	}
	scopeKey := evidenceScopeKey(binding)
	delta := evidenceUsageEvent{ScopeKey: scopeKey, Calls: 1, Opens: opens, ReadBytes: len(response)}
	events := []wal.Event{}
	usageRaw, _ := json.Marshal(delta)
	events = append(events, wal.Event{Store: "evidence", Type: "usage", Session: binding.sessionID, Data: usageRaw})
	if operation == "open" {
		opened, ok := value.(EvidenceOpened)
		if !ok {
			return errors.New("invalid evidence open response")
		}
		if len(opened.Body) > maxEvidenceOpenBytes {
			return errors.New("evidence opened body exceeds limit")
		}
		receipt := evidenceReceipt{Schema: evidenceSchema, ReceiptID: opened.ReceiptID, SessionID: binding.sessionID, Generation: binding.generation,
			ParentID: s.evidenceParent(binding.sessionID), Plugin: binding.identity.Canonical, Ref: opened.Ref,
			OpenedBody: opened.Body, OpenedDigest: evidenceDigest([]byte(opened.Body))}
		if receipt.ReceiptID == "" || receipt.ReceiptID != evidenceReceiptID(binding, opened) {
			return errors.New("invalid evidence receipt identity")
		}
		raw, _ := json.Marshal(receipt)
		events = append(events, wal.Event{Store: "evidence", Type: "read", Session: binding.sessionID, Data: raw})
	}
	fingerprint := evidenceDigest(append(append(append([]byte(scopeKey+"\x00"+operation+"\x00"), request...), 0), response...))
	tx := wal.Transaction{ID: "evidence-" + strings.TrimPrefix(fingerprint, "sha256:"), IdempotencyKey: fingerprint,
		Principal: binding.principal, Actor: binding.identity.Canonical, Events: events}
	// Bindings, budget folding, and the WAL append share one native critical
	// section. WAL.Append serializes bytes, but without this outer lock two
	// distinct requests could both observe the same remaining quota and exceed
	// it before either append became visible.
	state.mu.Lock()
	defer state.mu.Unlock()
	records := state.store.Records()
	for _, record := range records {
		if record.Transaction.IdempotencyKey == fingerprint {
			_, err := state.store.Append(tx)
			return err
		}
	}
	usage := foldEvidenceUsage(records, scopeKey)
	if usage.Calls+delta.Calls > maxEvidenceCalls || usage.Opens+delta.Opens > maxEvidenceOpens || usage.ReadBytes+delta.ReadBytes > maxEvidenceReadBytes {
		return errors.New("evidence aggregate budget exhausted")
	}
	_, err := state.store.Append(tx)
	return err
}

func evidenceReceiptID(binding artifactBinding, opened EvidenceOpened) string {
	openedDigest := evidenceDigest([]byte(opened.Body))
	return evidenceDigest([]byte(evidenceScopeKey(binding) + "\x00" + evidenceRefKey(opened.Ref) + "\x00" + openedDigest))
}

func (s *Service) resolveArtifactEvidenceReceipts(binding artifactBinding, ids []string) ([]string, error) {
	if len(ids) > 32 {
		return nil, errors.New("artifact evidence receipt ids exceed 32")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	available := make(map[string]evidenceReceipt)
	for _, record := range s.artifacts.store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store != "evidence" || event.Type != "read" || event.Session != binding.sessionID {
				continue
			}
			var receipt evidenceReceipt
			if json.Unmarshal(event.Data, &receipt) != nil || receipt.Schema != evidenceSchema ||
				receipt.SessionID != binding.sessionID || receipt.Generation != binding.generation ||
				receipt.Plugin != binding.identity.Canonical || receipt.ReceiptID == "" ||
				receipt.OpenedDigest != evidenceDigest([]byte(receipt.OpenedBody)) {
				continue
			}
			expected := evidenceReceiptID(binding, EvidenceOpened{Ref: receipt.Ref, Body: receipt.OpenedBody})
			if receipt.ReceiptID == expected {
				available[receipt.ReceiptID] = receipt
			}
		}
	}
	seen := make(map[string]bool, len(ids))
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] || !validSHA256Digest(id) {
			return nil, errors.New("artifact evidence receipt ids must be unique exact sha256 digests")
		}
		seen[id] = true
		if _, ok := available[id]; !ok {
			return nil, errors.New("artifact evidence receipt was not opened by this exact plugin session generation")
		}
		refs = append(refs, "broker:evidence-receipt:"+id)
	}
	return refs, nil
}

func (s *Service) evidenceParent(sessionID string) string {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	if state := s.sessions[sessionID]; state != nil {
		return state.parentID
	}
	return ""
}

func evidenceScopeKey(binding artifactBinding) string {
	return fmt.Sprintf("%s\x00%d\x00%s", binding.sessionID, binding.generation, binding.identity.Canonical)
}

func foldEvidenceUsage(records []wal.Record, scopeKey string) evidenceUsage {
	var usage evidenceUsage
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != "evidence" || event.Type != "usage" {
				continue
			}
			var delta evidenceUsageEvent
			if json.Unmarshal(event.Data, &delta) == nil && delta.ScopeKey == scopeKey {
				usage.Calls += delta.Calls
				usage.Opens += delta.Opens
				usage.ReadBytes += delta.ReadBytes
			}
		}
	}
	return usage
}

type evidenceCitation struct {
	Ref                EvidenceRef `json:"ref"`
	Excerpt            string      `json:"excerpt"`
	EntailmentVerified bool        `json:"entailment_verified"`
}
type evidenceClaim struct {
	Text      string             `json:"text"`
	Citations []evidenceCitation `json:"citations"`
}
type evidenceResearchResult struct {
	Answer           string          `json:"answer"`
	Claims           []evidenceClaim `json:"claims"`
	Conflicts        []string        `json:"conflicts,omitempty"`
	PossiblyStale    []string        `json:"possibly_stale,omitempty"`
	NotFound         []string        `json:"not_found,omitempty"`
	Confidence       string          `json:"confidence"`
	LearnSuggestions []string        `json:"learn_suggestions,omitempty"`
}

func (s *Service) validateEvidenceResult(parent artifactBinding, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > maxEvidenceResultBytes+(4<<10) {
		return nil, errors.New("research validation request exceeds limit")
	}
	var request struct {
		ChildSession string                 `json:"child_session"`
		Result       evidenceResearchResult `json:"result"`
	}
	if err := strictUnmarshal(raw, &request); err != nil {
		return nil, err
	}
	s.sessionsMu.RLock()
	child := s.sessions[request.ChildSession]
	validChild := child != nil && child.parentID == parent.sessionID && child.handle.Purpose == PurposeSubagent &&
		child.role == "explorer" && child.mode == "read_only"
	childGeneration := uint64(0)
	if child != nil {
		childGeneration = child.generation
	}
	s.sessionsMu.RUnlock()
	if !validChild {
		return nil, errors.New("citation child is not an exact direct read-only child")
	}
	receipts := evidenceReceipts(s.artifacts.store.Records(), request.ChildSession, childGeneration, parent.sessionID, parent.identity.Canonical)
	if len(request.Result.Answer) > 8<<10 || len(request.Result.Claims) > 64 {
		return nil, errors.New("research result exceeds structural limits")
	}
	switch request.Result.Confidence {
	case "low", "medium", "high":
	default:
		return nil, errors.New("research confidence must be low, medium, or high")
	}
	for claimIndex := range request.Result.Claims {
		claim := &request.Result.Claims[claimIndex]
		if strings.TrimSpace(claim.Text) == "" || len(claim.Text) > 4096 || len(claim.Citations) == 0 || len(claim.Citations) > 16 {
			return nil, fmt.Errorf("claim %d is invalid or uncited", claimIndex)
		}
		for citationIndex := range claim.Citations {
			citation := &claim.Citations[citationIndex]
			receipt, ok := receipts[evidenceRefKey(citation.Ref)]
			if !ok || receipt.Ref != citation.Ref {
				return nil, fmt.Errorf("claim %d citation %d was not opened", claimIndex, citationIndex)
			}
			if citation.Excerpt == "" || len(citation.Excerpt) > maxEvidenceExcerpt || !strings.Contains(receipt.OpenedBody, citation.Excerpt) {
				return nil, fmt.Errorf("claim %d citation %d excerpt is not an exact opened span", claimIndex, citationIndex)
			}
			citation.EntailmentVerified = false
		}
	}
	encoded, err := json.Marshal(request.Result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxEvidenceResultBytes {
		return nil, errors.New("validated research result exceeds limit")
	}
	return encoded, nil
}

func evidenceReceipts(records []wal.Record, sessionID string, generation uint64, parentID, plugin string) map[string]evidenceReceipt {
	out := map[string]evidenceReceipt{}
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != "evidence" || event.Type != "read" || event.Session != sessionID {
				continue
			}
			var receipt evidenceReceipt
			if json.Unmarshal(event.Data, &receipt) == nil && receipt.Schema == evidenceSchema && receipt.Generation == generation && receipt.ParentID == parentID && receipt.Plugin == plugin &&
				receipt.OpenedDigest == evidenceDigest([]byte(receipt.OpenedBody)) {
				out[evidenceRefKey(receipt.Ref)] = receipt
			}
		}
	}
	return out
}

func evidenceRefKey(ref EvidenceRef) string {
	raw, _ := json.Marshal(ref)
	return evidenceDigest(raw)
}
func evidenceDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func boundedEvidenceText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
