package artifacts

import (
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
	now    func() time.Time
}

func NewService(w Appender, grants *authority.Consumer) *Service {
	return &Service{wal: w, grants: grants, now: time.Now}
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

func (s *Service) Create(ctx context.Context, item Artifact, principal, actor, idem string) (Artifact, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" {
		item.ID = mintID()
	}
	item.Version = 1
	item.Authority = AuthorityCandidate
	item.CreatedAt, item.UpdatedAt = s.now().UTC(), s.now().UTC()
	if err := prepare(&item, principal); err != nil {
		return Artifact{}, err
	}
	if _, ok, err := s.showLocked(item.ID); err != nil {
		return Artifact{}, err
	} else if ok {
		return Artifact{}, fmt.Errorf("artifact %q already exists", item.ID)
	}
	data, _ := json.Marshal(createEvent{Artifact: item})
	_, err := s.wal.Append(transaction(principal, actor, idem, "artifact.create", data))
	return item, err
}

// Edit creates a candidate version. Editing an active version never silently
// replaces the active prompt-eligible head.
func (s *Service) Edit(ctx context.Context, id string, expected uint64, replacement Artifact, principal, actor, idem string) (Artifact, error) {
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
	replacement.ID = id
	replacement.Version = current.Version + 1
	replacement.Authority = AuthorityCandidate
	replacement.CreatedAt = current.CreatedAt
	replacement.UpdatedAt = s.now().UTC()
	replacement.Binding = current.Binding
	replacement.Scope = current.Scope
	if err := prepare(&replacement, principal); err != nil {
		return Artifact{}, err
	}
	data, _ := json.Marshal(replaceEvent{ID: id, ExpectedVersion: expected, Artifact: replacement})
	_, err = s.wal.Append(transaction(principal, actor, idem, "artifact.edit", data))
	return replacement, err
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
	items, err := fold(s.wal.Records())
	if err != nil {
		return nil, err
	}
	max := q.MaxItems
	if max <= 0 {
		max = 50
	}
	allowedKinds := map[Kind]bool{}
	for _, k := range q.Kinds {
		allowedKinds[k] = true
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
	if len(out) > max {
		out = out[:max]
	}
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
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != artifactStore {
				continue
			}
			switch ev.Type {
			case "artifact.create":
				var p createEvent
				if err := json.Unmarshal(ev.Data, &p); err != nil {
					return nil, err
				}
				items[p.Artifact.ID] = p.Artifact
			case "artifact.edit":
				var p replaceEvent
				if err := json.Unmarshal(ev.Data, &p); err != nil {
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

func prepare(a *Artifact, principal string) error {
	if a.Kind != KindMemory && a.Kind != KindLesson {
		return errors.New("artifact kind must be memory or lesson")
	}
	if strings.TrimSpace(a.Summary) == "" {
		return errors.New("artifact summary required")
	}
	if a.Kind == KindLesson && strings.TrimSpace(a.Trigger) == "" {
		return errors.New("lesson trigger required")
	}
	if a.Binding.Principal == "" {
		a.Binding.Principal = principal
	}
	if a.Binding.Principal != principal {
		return errors.New("artifact principal is host-bound")
	}
	switch a.Scope {
	case ScopeGlobal:
		if a.Binding.CanonicalRepoID != "" || a.Binding.AnchorSessionID != "" {
			return errors.New("global scope has no repo/session binding")
		}
	case ScopeRepo:
		if a.Binding.CanonicalRepoID == "" || a.Binding.AnchorSessionID != "" {
			return errors.New("repo scope requires only canonical repo id")
		}
	case ScopeSession:
		if a.Binding.AnchorSessionID == "" {
			return errors.New("session scope requires anchor session id")
		}
	default:
		return errors.New("invalid artifact scope")
	}
	var err error
	a.Tags, err = normalizeLabels(a.Tags, maxTags)
	if err != nil {
		return err
	}
	a.Groups, err = normalizeGroups(a.Groups)
	if err != nil {
		return err
	}
	if a.Sensitivity == "" {
		a.Sensitivity = "normal"
	}
	if a.Sensitivity != "normal" && a.Sensitivity != "private" && a.Sensitivity != "secret" {
		return errors.New("invalid sensitivity")
	}
	return nil
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
	case AuthorityLegacyActive:
		return to == AuthorityCandidate || to == AuthorityActive || to == AuthorityRetired
	}
	return false
}
func transaction(principal, actor, idem, typ string, data []byte) wal.Transaction {
	return wal.Transaction{ID: mintID(), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: artifactStore, Type: typ, Data: data}}}
}
func mintID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "art_" + hex.EncodeToString(b[:])
}
