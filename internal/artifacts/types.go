// Package artifacts implements the plugin-defined, versioned harness artifact
// envelope from EP-0053 and EP-0063. Canonical ordering and durability belong
// to broker/wal; plugin manifests define only the dynamic data object.
package artifacts

import (
	"encoding/json"
	"time"
)

const APIVersionV1 = "stado.dev/artifact/v1"

type Kind string

type KindSchema struct {
	PluginIdentity string `json:"plugin_identity"`
	PluginCommit   string `json:"plugin_commit,omitempty"`
	ManifestDigest string `json:"manifest_digest"`
	LocalName      string `json:"local_name"`
	SchemaDigest   string `json:"schema_digest"`
}

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

// Provenance records mechanically assigned origins separately from the actor
// that created this artifact version and the causal records that led to it.
// It is host-owned envelope data (EP-0053/EP-0063), not part of a plugin's
// dynamic data schema.
type Provenance struct {
	Origins   []string `json:"origins,omitempty"`
	CreatedBy string   `json:"created_by,omitempty"`
	Refs      []string `json:"refs,omitempty"`
}

// UnmarshalJSON accepts the pre-EP-0063 string-array shape only for one-way
// migration. New marshals always emit the structured object, so old records
// remain readable without preserving the obsolete write contract.
func (p *Provenance) UnmarshalJSON(raw []byte) error {
	type plain Provenance
	var object plain
	if err := json.Unmarshal(raw, &object); err == nil {
		*p = Provenance(object)
		return nil
	}
	var legacy []string
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return err
	}
	*p = Provenance{Origins: legacy}
	return nil
}

type Artifact struct {
	APIVersion   string          `json:"api_version"`
	ID           string          `json:"id"`
	Version      uint64          `json:"version"`
	Kind         Kind            `json:"kind"`
	KindSchema   KindSchema      `json:"kind_schema"`
	Scope        Scope           `json:"scope"`
	Binding      ScopeBinding    `json:"scope_binding"`
	Authority    Authority       `json:"authority"`
	Tags         []string        `json:"tags,omitempty"`
	Groups       []string        `json:"groups,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Sensitivity  string          `json:"sensitivity"`
	Provenance   Provenance      `json:"provenance"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ExpiresAt    time.Time       `json:"expires_at,omitempty"`
	Supersedes   []string        `json:"supersedes,omitempty"`
	LegacyID     string          `json:"legacy_id,omitempty"`
}

// UnmarshalJSON accepts the old EP-53 memory/lesson top-level fields only as a
// migration reader. Marshal always emits the EP-63 envelope; new writes never
// perpetuate the old shape.
func (a *Artifact) UnmarshalJSON(raw []byte) error {
	type plain Artifact
	var wire struct {
		plain
		Summary         string `json:"summary"`
		Content         string `json:"content"`
		Trigger         string `json:"trigger"`
		ExpectedOutcome string `json:"expected_outcome"`
		Validation      string `json:"validation"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*a = Artifact(wire.plain)
	if a.APIVersion == "" {
		a.APIVersion = APIVersionV1
	}
	if a.Kind == legacyKindMemory || a.Kind == legacyKindLesson {
		legacy := a.Kind
		if legacy == legacyKindMemory {
			a.Kind = KindMemory
		} else {
			a.Kind = KindLesson
		}
		if len(a.Data) == 0 {
			a.Data = mustLearningData(LearningData{
				Summary: wire.Summary, Content: wire.Content, Trigger: wire.Trigger,
				ExpectedOutcome: wire.ExpectedOutcome, Validation: wire.Validation,
			})
		}
		if a.KindSchema.LocalName == "" {
			a.KindSchema = learningDescriptorFor(a.Kind).Schema
		}
	}
	return nil
}

type QueryContext struct {
	Principal          string
	CanonicalRepoID    string
	SessionID          string
	AncestorSessionIDs []string
}

// ArtifactRef selects one immutable artifact version. Query callers use Refs
// when correctness depends on an exact object (for example, recovering
// application-local configuration) rather than searching a recency page.
type ArtifactRef struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

type Query struct {
	Context    QueryContext
	Refs       []ArtifactRef
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
	Provenance Provenance   `json:"provenance"`
}
