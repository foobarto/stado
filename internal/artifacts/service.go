package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"golang.org/x/text/unicode/norm"
)

const (
	artifactStore = "artifact"
	maxDataBytes  = 1 << 20
	maxTags       = 32
	maxGroups     = 16
	maxLabelBytes = 96
)

var (
	ErrNotFound      = errors.New("artifact not found")
	ErrVersion       = errors.New("artifact version conflict")
	ErrOperatorGrant = authority.ErrGrantRequired
)

// Appender is satisfied by wal.Store and keeps projection tests independent.
type Appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}

// Service folds artifact events from the canonical WAL.
type Service struct {
	mu     sync.Mutex
	wal    Appender
	grants *authority.Consumer
	kinds  *KindRegistry
	now    func() time.Time
}

func NewService(w Appender, grants *authority.Consumer) *Service {
	return NewServiceWithKinds(w, grants, NewKindRegistry())
}

func NewServiceWithKinds(w Appender, grants *authority.Consumer, kinds *KindRegistry) *Service {
	return &Service{wal: w, grants: grants, kinds: kinds, now: time.Now}
}

type createEvent struct {
	Artifact Artifact `json:"artifact"`
}
type replaceEvent struct {
	ID              string   `json:"id"`
	ExpectedVersion uint64   `json:"expected_version"`
	Artifact        Artifact `json:"artifact"`
}
type authorityEvent struct {
	ID              string    `json:"id"`
	ExpectedVersion uint64    `json:"expected_version"`
	Authority       Authority `json:"authority"`
	GrantID         string    `json:"operator_grant_id,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type kindEvent struct {
	Descriptor KindDescriptor `json:"descriptor"`
}

func (s *Service) Create(ctx context.Context, item Artifact, principal, actor, idem string) (Artifact, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	item.Version = 1
	item.Authority = AuthorityCandidate
	desc, err := s.prepare(&item, principal)
	if err != nil {
		return Artifact{}, err
	}
	if prior, found, err := priorCreate(s.wal.Records(), idem, item, principal); found || err != nil {
		return prior, err
	}
	if item.ID == "" {
		item.ID = mintID()
	}
	item.CreatedAt, item.UpdatedAt = s.now().UTC(), s.now().UTC()
	if _, ok, err := s.showLocked(item.ID); err != nil {
		return Artifact{}, err
	} else if ok {
		return Artifact{}, fmt.Errorf("artifact %q already exists", item.ID)
	}
	data, _ := json.Marshal(createEvent{Artifact: item})
	events := s.kindRegistrationEvents(desc)
	events = append(events, wal.Event{Store: artifactStore, Type: "artifact.create", Data: data})
	_, err = s.wal.Append(transactionEvents(principal, actor, idem, events))
	return item, err
}

// Edit creates a candidate version. Editing an active version never silently
// replaces the active prompt-eligible head.
func (s *Service) Edit(ctx context.Context, id string, expected uint64, replacement Artifact, principal, actor, idem string) (Artifact, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := normalizeEditRetryInput(&replacement); err != nil {
		return Artifact{}, err
	}
	if prior, found, err := priorEdit(s.wal.Records(), idem, id, expected, replacement, principal); found || err != nil {
		return prior, err
	}
	current, ok, err := s.showLocked(id)
	if err != nil {
		return Artifact{}, err
	}
	if !ok {
		return Artifact{}, ErrNotFound
	}
	if current.Version != expected {
		return Artifact{}, ErrVersion
	}
	if current.Authority == AuthorityDeleted {
		return Artifact{}, errors.New("deleted artifact is a terminal tombstone")
	}
	replacement.ID = id
	replacement.Version = current.Version + 1
	replacement.Authority = AuthorityCandidate
	replacement.CreatedAt = current.CreatedAt
	replacement.UpdatedAt = s.now().UTC()
	replacement.Binding = current.Binding
	replacement.Scope = current.Scope
	replacement.Kind = current.Kind
	desc, err := s.prepare(&replacement, principal)
	if err != nil {
		return Artifact{}, err
	}
	data, _ := json.Marshal(replaceEvent{ID: id, ExpectedVersion: expected, Artifact: replacement})
	events := s.kindRegistrationEvents(desc)
	events = append(events, wal.Event{Store: artifactStore, Type: "artifact.edit", Data: data})
	_, err = s.wal.Append(transactionEvents(principal, actor, idem, events))
	return replacement, err
}

func normalizeEditRetryInput(item *Artifact) error {
	var err error
	item.Tags, err = normalizeLabels(item.Tags, maxTags)
	if err != nil {
		return err
	}
	item.Groups, err = normalizeGroups(item.Groups)
	if err != nil {
		return err
	}
	item.EvidenceRefs, err = normalizeRefs(item.EvidenceRefs, 64, "evidence reference")
	if err != nil {
		return err
	}
	item.Sensitivity = normalizedSensitivity(item.Sensitivity)
	return validateProvenance(item.Provenance)
}

// priorCreate and priorEdit make broker transport retries return the exact
// result that was durably committed. WAL idempotency alone cannot do that:
// artifact IDs, transaction IDs, and timestamps are host-minted, so rebuilding
// a transaction after a lost response would correctly look different to the
// WAL and report a conflict. These helpers compare only caller-controlled
// mutation input, reject logical-key reuse with different input, and return the
// immutable result recorded by the first transaction.
func priorCreate(records []wal.Record, idem string, requested Artifact, principal string) (Artifact, bool, error) {
	if idem == "" {
		return Artifact{}, false, nil
	}
	for _, record := range records {
		if record.Transaction.IdempotencyKey != idem {
			continue
		}
		if record.Transaction.Principal != principal {
			return Artifact{}, true, wal.ErrConflict
		}
		for _, event := range record.Transaction.Events {
			if event.Store != artifactStore || event.Type != "artifact.create" {
				continue
			}
			var committed createEvent
			if err := json.Unmarshal(event.Data, &committed); err != nil {
				return Artifact{}, true, err
			}
			if !sameCreateInput(committed.Artifact, requested) {
				return Artifact{}, true, wal.ErrConflict
			}
			return committed.Artifact, true, nil
		}
		return Artifact{}, true, wal.ErrConflict
	}
	return Artifact{}, false, nil
}

func priorEdit(records []wal.Record, idem, id string, expected uint64, requested Artifact, principal string) (Artifact, bool, error) {
	if idem == "" {
		return Artifact{}, false, nil
	}
	for _, record := range records {
		if record.Transaction.IdempotencyKey != idem {
			continue
		}
		if record.Transaction.Principal != principal {
			return Artifact{}, true, wal.ErrConflict
		}
		for _, event := range record.Transaction.Events {
			if event.Store != artifactStore || event.Type != "artifact.edit" {
				continue
			}
			var committed replaceEvent
			if err := json.Unmarshal(event.Data, &committed); err != nil {
				return Artifact{}, true, err
			}
			if committed.ID != id || committed.ExpectedVersion != expected || !sameEditInput(committed.Artifact, requested) {
				return Artifact{}, true, wal.ErrConflict
			}
			return committed.Artifact, true, nil
		}
		return Artifact{}, true, wal.ErrConflict
	}
	return Artifact{}, false, nil
}

func sameCreateInput(committed, requested Artifact) bool {
	return committed.Kind == requested.Kind && committed.Scope == requested.Scope &&
		committed.Binding == requested.Binding && slicesEqual(committed.Tags, requested.Tags) &&
		slicesEqual(committed.Groups, requested.Groups) && slicesEqual(committed.EvidenceRefs, requested.EvidenceRefs) &&
		committed.Sensitivity == normalizedSensitivity(requested.Sensitivity) &&
		bytes.Equal(committed.Data, requested.Data) && committed.ExpiresAt.Equal(requested.ExpiresAt)
}

func sameEditInput(committed, requested Artifact) bool {
	return slicesEqual(committed.Tags, requested.Tags) && slicesEqual(committed.Groups, requested.Groups) &&
		slicesEqual(committed.EvidenceRefs, requested.EvidenceRefs) && committed.Sensitivity == normalizedSensitivity(requested.Sensitivity) &&
		bytes.Equal(committed.Data, requested.Data) && committed.ExpiresAt.Equal(requested.ExpiresAt)
}

func normalizedSensitivity(value string) string {
	if value == "" {
		return "normal"
	}
	return value
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (s *Service) SetAuthority(ctx context.Context, id string, expected uint64, next Authority, grantID, principal, actor, idem string) (Artifact, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok, err := s.showLocked(id)
	if err != nil {
		return Artifact{}, err
	}
	if !ok {
		return Artifact{}, ErrNotFound
	}
	if current.Version != expected {
		return Artifact{}, ErrVersion
	}
	if current.Authority == AuthorityDeleted {
		return Artifact{}, errors.New("deleted artifact is a terminal tombstone")
	}
	if !validTransition(current.Authority, next) {
		return Artifact{}, fmt.Errorf("invalid authority transition %s -> %s", current.Authority, next)
	}
	updatedAt := s.now().UTC()
	data, _ := json.Marshal(authorityEvent{ID: id, ExpectedVersion: expected, Authority: next, GrantID: grantID, UpdatedAt: updatedAt})
	tx := transaction(principal, actor, idem, "artifact.authority", data)
	if next == AuthorityActive {
		if strings.TrimSpace(grantID) == "" || s.grants == nil {
			return Artifact{}, ErrOperatorGrant
		}
		action, err := ActivationAction(current, principal)
		if err != nil {
			return Artifact{}, err
		}
		grantEvent, err := s.grants.PrepareConsume(ctx, grantID, action, idem)
		if err != nil {
			return Artifact{}, err
		}
		if grantEvent.Type != "" {
			tx.Events = append(tx.Events, grantEvent)
		}
	}
	if _, err := s.wal.Append(tx); err != nil {
		return Artifact{}, err
	}
	current.Version++
	current.Authority = next
	current.UpdatedAt = updatedAt
	return current, nil
}

// ActivationAction returns the exact action an operator must approve.
func ActivationAction(item Artifact, principal string) (authority.Action, error) {
	scopeBytes, err := json.Marshal(struct {
		Scope   Scope        `json:"scope"`
		Binding ScopeBinding `json:"binding"`
	}{item.Scope, item.Binding})
	if err != nil {
		return authority.Action{}, err
	}
	payloadBytes, err := json.Marshal(item)
	if err != nil {
		return authority.Action{}, err
	}
	scopeSum := sha256.Sum256(scopeBytes)
	payloadSum := sha256.Sum256(payloadBytes)
	return authority.Action{Kind: "artifact.activate", Principal: principal, ScopeDigest: hex.EncodeToString(scopeSum[:]), PayloadDigest: hex.EncodeToString(payloadSum[:]), Version: item.Version}, nil
}

func (s *Service) Show(id string) (Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.showLocked(id)
}

// Visible returns one exact artifact version only when it is visible from the
// broker-supplied query context. Callers that authorize an edit, observation,
// or relation must not approximate this check by searching a bounded result
// page: an older visible artifact can legitimately sort after that page.
func (s *Service) Visible(id string, version uint64, qctx QueryContext) (Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok, err := s.showLocked(id)
	if err != nil || !ok || a.Version != version || !scopeMatches(a, qctx) {
		return Artifact{}, false, err
	}
	if !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(s.now()) {
		return Artifact{}, false, nil
	}
	return a, true, nil
}

func (s *Service) showLocked(id string) (Artifact, bool, error) {
	items, err := fold(s.wal.Records())
	if err != nil {
		return Artifact{}, false, err
	}
	a, ok := items[id]
	return a, ok, nil
}

func (s *Service) Query(q Query) ([]Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.queryMatchesLocked(q)
	if err != nil {
		return nil, err
	}
	max := q.MaxItems
	if max <= 0 {
		max = 50
	}
	// Exact-reference queries are bounded by the number of requested immutable
	// refs, never by the default recency page. This prevents a valid selected
	// object from disappearing merely because newer unrelated artifacts exist.
	if len(q.Refs) > 0 && max < len(q.Refs) {
		max = len(q.Refs)
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// QueryPage returns one bounded page plus a digest of the complete matching
// (artifact id, version) projection. A caller fences every later offset with
// that digest, so concurrent edits cannot silently produce skipped or repeated
// rows. The broker owns request validation and the maximum page size; this
// service method keeps the snapshot calculation and fold under one lock.
func (s *Service) QueryPage(q Query, offset, limit int) (ArtifactPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if offset < 0 || limit <= 0 {
		return ArtifactPage{}, errors.New("artifact page offset and limit are invalid")
	}
	out, err := s.queryMatchesLocked(q)
	if err != nil {
		return ArtifactPage{}, err
	}
	if offset > len(out) {
		return ArtifactPage{}, errors.New("artifact page offset exceeds the matching projection")
	}
	refs := make([]ArtifactRef, len(out))
	for i := range out {
		refs[i] = ArtifactRef{ID: out[i].ID, Version: out[i].Version}
	}
	digestBytes, err := json.Marshal(refs)
	if err != nil {
		return ArtifactPage{}, err
	}
	sum := sha256.Sum256(digestBytes)
	page := ArtifactPage{Digest: "sha256:" + hex.EncodeToString(sum[:])}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	page.Items = append([]Artifact(nil), out[offset:end]...)
	page.Complete = end == len(out)
	if !page.Complete {
		page.NextOffset = end
	}
	return page, nil
}

type ArtifactPage struct {
	Items      []Artifact
	Digest     string
	NextOffset int
	Complete   bool
}

func (s *Service) queryMatchesLocked(q Query) ([]Artifact, error) {
	items, err := fold(s.wal.Records())
	if err != nil {
		return nil, err
	}
	allowedKinds := map[Kind]bool{}
	for _, k := range q.Kinds {
		allowedKinds[k] = true
	}
	allowedRefs := make(map[string]uint64, len(q.Refs))
	for _, ref := range q.Refs {
		allowedRefs[ref.ID] = ref.Version
	}
	tags, err := normalizeLabels(q.Tags, maxTags)
	if err != nil {
		return nil, err
	}
	groups, err := normalizeGroups(q.Groups)
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for _, a := range items {
		if q.ExcludeSecret && a.Sensitivity == "secret" {
			continue
		}
		if len(allowedRefs) > 0 && allowedRefs[a.ID] != a.Version {
			continue
		}
		if q.ActiveOnly && a.Authority != AuthorityActive {
			continue
		}
		if len(allowedKinds) > 0 && !allowedKinds[a.Kind] {
			continue
		}
		if !scopeMatches(a, q.Context) || !containsAll(a.Tags, tags) || !containsAll(a.Groups, groups) {
			continue
		}
		if !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(s.now()) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Service) RecordUsage(ctx context.Context, obs UsageObservation, principal, actor, idem string) (UsageObservation, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok, err := s.showLocked(obs.ArtifactID)
	if err != nil {
		return UsageObservation{}, err
	}
	if !ok || a.Version != obs.ArtifactVersion {
		return UsageObservation{}, ErrNotFound
	}
	if !validUsage(obs.Event) {
		return UsageObservation{}, errors.New("invalid usage event")
	}
	if (obs.Event == UsageHelped || obs.Event == UsageFailed) && strings.TrimSpace(obs.Evaluator) == "" {
		return UsageObservation{}, errors.New("evaluative usage requires external evaluator")
	}
	if obs.ID == "" {
		obs.ID = mintID()
	}
	obs.CreatedAt = s.now().UTC()
	b, _ := json.Marshal(obs)
	_, err = s.wal.Append(transaction(principal, actor, idem, "observation.record", b))
	return obs, err
}
func (s *Service) Usage(artifactID string) ([]UsageObservation, error) {
	var out []UsageObservation
	for _, rec := range s.wal.Records() {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != artifactStore || ev.Type != "observation.record" {
				continue
			}
			var o UsageObservation
			if err := json.Unmarshal(ev.Data, &o); err != nil {
				return nil, err
			}
			if o.ArtifactID == artifactID {
				out = append(out, o)
			}
		}
	}
	return out, nil
}
func validUsage(e UsageEvent) bool {
	switch e {
	case UsageConsidered, UsageSurfaced, UsageOpened, UsageCited, UsageFollowed, UsageContradicted, UsageHelped, UsageFailed:
		return true
	}
	return false
}

// Relate stores an edge only when both endpoints are visible in the writer's
// authenticated query context. Relation queries repeat that authorization and
// never reveal a hidden endpoint through counts or metadata.
func (s *Service) Relate(ctx context.Context, rel Relation, qctx QueryContext, principal, actor, idem string) (Relation, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := fold(s.wal.Records())
	if err != nil {
		return Relation{}, err
	}
	from, ok1 := items[rel.FromID]
	to, ok2 := items[rel.ToID]
	if !ok1 || !ok2 || !scopeMatches(from, qctx) || !scopeMatches(to, qctx) {
		return Relation{}, ErrNotFound
	}
	if !validRelation(rel.Type) || rel.FromID == rel.ToID {
		return Relation{}, errors.New("invalid artifact relation")
	}
	if rel.ID == "" {
		rel.ID = mintID()
	}
	rel.CreatedAt = s.now().UTC()
	b, _ := json.Marshal(rel)
	_, err = s.wal.Append(transaction(principal, actor, idem, "relation.created", b))
	return rel, err
}
func (s *Service) Relations(id string, qctx QueryContext) ([]Relation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := fold(s.wal.Records())
	if err != nil {
		return nil, err
	}
	anchor, ok := items[id]
	if !ok || !scopeMatches(anchor, qctx) {
		return nil, ErrNotFound
	}
	var out []Relation
	for _, rec := range s.wal.Records() {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != artifactStore || ev.Type != "relation.created" {
				continue
			}
			var r Relation
			if err := json.Unmarshal(ev.Data, &r); err != nil {
				return nil, err
			}
			if r.FromID != id && r.ToID != id {
				continue
			}
			from, ok1 := items[r.FromID]
			to, ok2 := items[r.ToID]
			if ok1 && ok2 && scopeMatches(from, qctx) && scopeMatches(to, qctx) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}
func validRelation(r RelationType) bool {
	return r == RelationRelated || r == RelationSupports || r == RelationContradicts || r == RelationSupersedes
}

func fold(records []wal.Record) (map[string]Artifact, error) {
	items := map[string]Artifact{}
	migrationStages := map[int]migrationStage{}
	migrationComplete := false
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != artifactStore {
				continue
			}
			switch ev.Type {
			case legacyMigrationStage:
				if migrationComplete {
					return nil, errors.New("legacy migration stage appeared after completion")
				}
				var stage migrationStage
				if err := strictLegacyJSON(ev.Data, &stage); err != nil {
					return nil, err
				}
				if _, duplicate := migrationStages[stage.Index]; duplicate {
					return nil, errors.New("duplicate legacy migration stage")
				}
				if err := validateMigrationStage(stage); err != nil {
					return nil, err
				}
				migrationStages[stage.Index] = stage
			case legacyCompletedEvent:
				if migrationComplete {
					return nil, errors.New("multiple completed legacy migrations")
				}
				var marker migrationCompleted
				if err := strictLegacyJSON(ev.Data, &marker); err != nil {
					return nil, err
				}
				projected, err := applyCompletedMigration(items, migrationStages, marker)
				if err != nil {
					return nil, err
				}
				items = projected
				migrationComplete = true
			case "artifact.create":
				p, err := decodeArtifactCreateEvent(ev.Data)
				if err != nil {
					return nil, err
				}
				items[p.Artifact.ID] = p.Artifact
			case "artifact.edit":
				p, err := decodeArtifactEditEvent(ev.Data)
				if err != nil {
					return nil, err
				}
				cur, ok := items[p.ID]
				if !ok || cur.Version != p.ExpectedVersion {
					return nil, ErrVersion
				}
				items[p.ID] = p.Artifact
			case "artifact.authority":
				var p authorityEvent
				if err := json.Unmarshal(ev.Data, &p); err != nil {
					return nil, err
				}
				cur, ok := items[p.ID]
				if !ok || cur.Version != p.ExpectedVersion {
					return nil, ErrVersion
				}
				cur.Version++
				cur.Authority = p.Authority
				cur.UpdatedAt = p.UpdatedAt
				items[p.ID] = cur
			}
		}
	}
	return items, nil
}

func (s *Service) prepare(a *Artifact, principal string) (KindDescriptor, error) {
	if len(a.Data) == 0 || len(a.Data) > maxDataBytes {
		return KindDescriptor{}, fmt.Errorf("artifact data size must be 1..%d bytes", maxDataBytes)
	}
	desc, err := s.kinds.Validate(a.Kind, a.Data)
	if err != nil {
		return KindDescriptor{}, err
	}
	a.APIVersion = APIVersionV1
	a.KindSchema = desc.Schema
	if a.Binding.Principal == "" {
		a.Binding.Principal = principal
	}
	if a.Binding.Principal != principal {
		return KindDescriptor{}, errors.New("artifact principal is host-bound")
	}
	switch a.Scope {
	case ScopeGlobal:
		if a.Binding.CanonicalRepoID != "" || a.Binding.AnchorSessionID != "" {
			return KindDescriptor{}, errors.New("global scope has no repo/session binding")
		}
	case ScopeRepo:
		if a.Binding.CanonicalRepoID == "" || a.Binding.AnchorSessionID != "" {
			return KindDescriptor{}, errors.New("repo scope requires only canonical repo id")
		}
	case ScopeSession:
		if a.Binding.AnchorSessionID == "" {
			return KindDescriptor{}, errors.New("session scope requires anchor session id")
		}
	default:
		return KindDescriptor{}, errors.New("invalid artifact scope")
	}
	a.Tags, err = normalizeLabels(a.Tags, maxTags)
	if err != nil {
		return KindDescriptor{}, err
	}
	a.Groups, err = normalizeGroups(a.Groups)
	if err != nil {
		return KindDescriptor{}, err
	}
	a.EvidenceRefs, err = normalizeRefs(a.EvidenceRefs, 64, "evidence reference")
	if err != nil {
		return KindDescriptor{}, err
	}
	a.Supersedes, err = normalizeRefs(a.Supersedes, 64, "superseded artifact id")
	if err != nil {
		return KindDescriptor{}, err
	}
	a.Provenance.Origins, err = normalizeRefs(a.Provenance.Origins, 32, "provenance origin")
	if err != nil {
		return KindDescriptor{}, err
	}
	a.Provenance.Refs, err = normalizeRefs(a.Provenance.Refs, 32, "provenance reference")
	if err != nil {
		return KindDescriptor{}, err
	}
	if a.Sensitivity == "" {
		a.Sensitivity = "normal"
	}
	if a.Sensitivity != "normal" && a.Sensitivity != "private" && a.Sensitivity != "secret" {
		return KindDescriptor{}, errors.New("invalid sensitivity")
	}
	if err := validateProvenance(a.Provenance); err != nil {
		return KindDescriptor{}, err
	}
	return desc, nil
}

func validateProvenance(p Provenance) error {
	if len(p.Origins) > 32 || len(p.Refs) > 32 {
		return errors.New("artifact provenance exceeds 32 origins or refs")
	}
	for _, value := range append(append([]string(nil), p.Origins...), p.Refs...) {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 512 {
			return errors.New("invalid artifact provenance value")
		}
	}
	if strings.TrimSpace(p.CreatedBy) != p.CreatedBy || len(p.CreatedBy) > 256 {
		return errors.New("invalid artifact provenance creator")
	}
	return nil
}

func normalizeRefs(in []string, max int, label string) ([]string, error) {
	if len(in) > max {
		return nil, fmt.Errorf("too many %ss: %d > %d", label, len(in), max)
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 512 {
			return nil, fmt.Errorf("invalid %s", label)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *Service) kindRegistrationEvents(desc KindDescriptor) []wal.Event {
	for _, rec := range s.wal.Records() {
		for _, event := range rec.Transaction.Events {
			if event.Store != artifactStore || event.Type != "kind.registered" {
				continue
			}
			var prior kindEvent
			if json.Unmarshal(event.Data, &prior) == nil &&
				prior.Descriptor.Kind == desc.Kind &&
				prior.Descriptor.Schema.SchemaDigest == desc.Schema.SchemaDigest &&
				prior.Descriptor.Schema.PluginIdentity == desc.Schema.PluginIdentity {
				return nil
			}
		}
	}
	raw, _ := json.Marshal(kindEvent{Descriptor: desc})
	return []wal.Event{{Store: artifactStore, Type: "kind.registered", Data: raw}}
}

func normalizeLabels(in []string, max int) ([]string, error) {
	if len(in) > max {
		return nil, fmt.Errorf("too many labels: %d > %d", len(in), max)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(norm.NFKC.String(raw)))
		s = strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ':' || r == '-' || r == '_' {
				return r
			}
			return -1
		}, s)
		if s == "" || len(s) > maxLabelBytes {
			return nil, errors.New("invalid label")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}
func normalizeGroups(in []string) ([]string, error) {
	out, err := normalizeLabels(in, maxGroups)
	if err != nil {
		return nil, err
	}
	for _, g := range out {
		if strings.HasPrefix(g, "/") || strings.HasSuffix(g, "/") {
			return nil, errors.New("invalid group")
		}
	}
	return out, nil
}
func containsAll(have, want []string) bool {
	m := map[string]bool{}
	for _, x := range have {
		m[x] = true
	}
	for _, x := range want {
		if !m[x] {
			return false
		}
	}
	return true
}
func scopeMatches(a Artifact, q QueryContext) bool {
	if a.Binding.Principal != q.Principal {
		return false
	}
	switch a.Scope {
	case ScopeGlobal:
		return true
	case ScopeRepo:
		return a.Binding.CanonicalRepoID == q.CanonicalRepoID
	case ScopeSession:
		if a.Binding.AnchorSessionID == q.SessionID {
			return true
		}
		for _, x := range q.AncestorSessionIDs {
			if x == a.Binding.AnchorSessionID {
				return true
			}
		}
	}
	return false
}
func validTransition(from, to Authority) bool {
	if to == AuthorityDeleted {
		return true
	}
	switch from {
	case AuthorityCandidate:
		return to == AuthorityActive || to == AuthorityRejected
	case AuthorityActive:
		return to == AuthorityCandidate || to == AuthoritySuperseded || to == AuthorityRetired
	}
	return false
}
func transaction(principal, actor, idem, typ string, data []byte) wal.Transaction {
	return transactionEvents(principal, actor, idem, []wal.Event{{Store: artifactStore, Type: typ, Data: data}})

}
func transactionEvents(principal, actor, idem string, events []wal.Event) wal.Transaction {
	return wal.Transaction{ID: mintID(), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: events}
}
func mintID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "art_" + hex.EncodeToString(b[:])
}
