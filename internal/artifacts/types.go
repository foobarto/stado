// Package artifacts implements the versioned memory/lesson projection defined
// by EP-0053. Canonical ordering and durability belong to broker/wal.
package artifacts

import "time"

type Kind string

const (
	KindMemory Kind = "memory"
	KindLesson Kind = "lesson"
)

type Authority string

const (
	AuthorityCandidate    Authority = "candidate"
	AuthorityActive       Authority = "active"
	AuthorityLegacyActive Authority = "legacy_active"
	AuthorityRejected     Authority = "rejected"
	AuthoritySuperseded   Authority = "superseded"
	AuthorityRetired      Authority = "retired"
	AuthorityDeleted      Authority = "deleted"
)

type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeRepo    Scope = "repo"
	ScopeSession Scope = "session"
)

type ScopeBinding struct {
	Principal       string `json:"principal"`
	CanonicalRepoID string `json:"canonical_repo_id,omitempty"`
	AnchorSessionID string `json:"anchor_session_id,omitempty"`
	AnchorForkPoint string `json:"anchor_fork_point,omitempty"`
}

type Artifact struct {
	ID              string       `json:"id"`
	Version         uint64       `json:"version"`
	Kind            Kind         `json:"kind"`
	Scope           Scope        `json:"scope"`
	Binding         ScopeBinding `json:"scope_binding"`
	Authority       Authority    `json:"authority"`
	Summary         string       `json:"summary"`
	Content         string       `json:"content,omitempty"`
	Trigger         string       `json:"trigger,omitempty"`
	Tags            []string     `json:"tags,omitempty"`
	Groups          []string     `json:"groups,omitempty"`
	EvidenceRefs    []string     `json:"evidence_refs,omitempty"`
	Sensitivity     string       `json:"sensitivity"`
	Provenance      []string     `json:"provenance,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	ExpiresAt       time.Time    `json:"expires_at,omitempty"`
	Supersedes      []string     `json:"supersedes,omitempty"`
	LegacyID        string       `json:"legacy_id,omitempty"`
	ExpectedOutcome string       `json:"expected_outcome,omitempty"`
	Validation      string       `json:"validation,omitempty"`
}

type QueryContext struct {
	Principal          string
	CanonicalRepoID    string
	SessionID          string
	AncestorSessionIDs []string
}

type Query struct {
	Context    QueryContext
	Kinds      []Kind
	Tags       []string
	Groups     []string
	ActiveOnly bool
	MaxItems   int
}

type UsageEvent string

const (
	UsageConsidered   UsageEvent = "considered"
	UsageSurfaced     UsageEvent = "surfaced"
	UsageOpened       UsageEvent = "opened"
	UsageCited        UsageEvent = "cited"
	UsageFollowed     UsageEvent = "followed"
	UsageContradicted UsageEvent = "contradicted"
	UsageHelped       UsageEvent = "helped"
	UsageFailed       UsageEvent = "failed"
)

type UsageObservation struct {
	ID              string     `json:"id"`
	ArtifactID      string     `json:"artifact_id"`
	ArtifactVersion uint64     `json:"artifact_version"`
	Event           UsageEvent `json:"event"`
	SessionID       string     `json:"session_id"`
	Turn            int        `json:"turn"`
	EvidenceRef     string     `json:"evidence_ref"`
	Evaluator       string     `json:"evaluator,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type RelationType string

const (
	RelationRelated     RelationType = "related"
	RelationSupports    RelationType = "supports"
	RelationContradicts RelationType = "contradicts"
	RelationSupersedes  RelationType = "supersedes"
)

type Relation struct {
	ID         string       `json:"id"`
	FromID     string       `json:"from_id"`
	ToID       string       `json:"to_id"`
	Type       RelationType `json:"type"`
	CreatedAt  time.Time    `json:"created_at"`
	Provenance []string     `json:"provenance,omitempty"`
}
