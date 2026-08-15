package artifacts

// This file is the only reader for the retired pre-EP-0063 memory formats.
// Generic Artifact JSON decoding deliberately does not recognize those shapes.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

const (
	LegacyConverterVersion = 1
	legacyMigrationStage   = "artifact.migration.stage"
	legacyCompletedEvent   = "migration.completed.legacy-memory-v1"
	maxLegacyEventBytes    = 2 << 20
	maxLegacyStageBytes    = 4 << 20
)

type LegacyMigrationIdentity struct {
	Runtime plugins.RuntimeIdentity `json:"runtime"`
	Memory  KindDescriptor          `json:"memory"`
	Lesson  KindDescriptor          `json:"lesson"`
}

func (i LegacyMigrationIdentity) validate() error {
	if err := i.Runtime.Validate(); err != nil {
		return fmt.Errorf("legacy migration runtime identity: %w", err)
	}
	for local, descriptor := range map[string]KindDescriptor{"memory": i.Memory, "lesson": i.Lesson} {
		qualified, err := i.Runtime.QualifiedKind(local)
		if err != nil {
			return err
		}
		if descriptor.Kind != Kind(qualified) || descriptor.Schema.PluginIdentity != i.Runtime.Canonical ||
			descriptor.Schema.PluginCommit != i.Runtime.ResolvedCommit || descriptor.Schema.ManifestDigest != i.Runtime.ManifestDigest ||
			descriptor.Schema.LocalName != local || descriptor.Schema.SchemaDigest != descriptor.Definition.SchemaDigest() {
			return fmt.Errorf("legacy migration %s descriptor is not bound to the exact runtime identity and schema digest", local)
		}
		if err := descriptor.Definition.Validate(); err != nil {
			return fmt.Errorf("legacy migration %s descriptor: %w", local, err)
		}
	}
	return nil
}

type LegacyMigration struct {
	RawLog        []byte
	ArchiveDigest string
	Principal     string
	Actor         string
	Identity      LegacyMigrationIdentity
	// ResolveSessionAnchor maps one historical logical git-session subject to
	// the exact broker-restored native session anchor. An unresolved subject is
	// quarantined; it is never broadened to repo/global visibility.
	ResolveSessionAnchor func(string) (string, bool)
	// ValidateSource is broker-owned and re-opens/re-hashes the fixed source
	// immediately before the completion marker. The broker holds its migration
	// adapter mutex across this callback and the whole migration call.
	ValidateSource func() error
}

type MigrationResult struct {
	Migrated      int                     `json:"migrated"`
	Quarantined   []string                `json:"quarantined,omitempty"`
	ArchiveDigest string                  `json:"archive_digest"`
	SourceDigest  string                  `json:"source_digest"`
	Identity      LegacyMigrationIdentity `json:"identity"`
	Complete      bool                    `json:"complete"`
}

type migrationCompleted struct {
	Converter int                 `json:"converter_version"`
	Result    MigrationResult     `json:"result"`
	Stages    []migrationStageRef `json:"stages"`
}

// CompletedLegacyMigration checks the permanent WAL fence without consulting
// the retired source. Once present, callers must return this exact durable
// result for the same identity and reject every other identity.
func (s *Service) CompletedLegacyMigration(identity LegacyMigrationIdentity) (MigrationResult, bool, error) {
	if err := identity.validate(); err != nil {
		return MigrationResult{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *MigrationResult
	for _, record := range s.wal.Records() {
		for _, event := range record.Transaction.Events {
			if event.Store != artifactStore || event.Type != legacyCompletedEvent {
				continue
			}
			var marker migrationCompleted
			if err := strictLegacyJSON(event.Data, &marker); err != nil || marker.Converter != LegacyConverterVersion || !marker.Result.Complete {
				return MigrationResult{}, false, errors.New("invalid completed legacy migration marker")
			}
			if found != nil {
				return MigrationResult{}, false, errors.New("multiple completed legacy migration markers")
			}
			copy := marker.Result
			found = &copy
		}
	}
	if found == nil {
		return MigrationResult{}, false, nil
	}
	if !sameMigrationIdentity(found.Identity, identity) {
		return MigrationResult{}, false, errors.New("legacy migration is permanently fenced to a different signed application identity")
	}
	if _, err := fold(s.wal.Records()); err != nil {
		return MigrationResult{}, false, fmt.Errorf("completed legacy migration stage set: %w", err)
	}
	return *found, true, nil
}

// MigrateLegacy decodes both authoritative historical sources (the fixed
// JSONL bytes and any pre-EP-0063 artifact heads in this WAL), validates the
// entire folded set, appends bounded inert stages, then commits one completion
// marker that makes the exact ordered stage set visible atomically. Any
// malformed, unbound, duplicate, or divergent record aborts without a visible
// canonical write.
func (s *Service) MigrateLegacy(ctx context.Context, req LegacyMigration) (MigrationResult, error) {
	_ = ctx
	if req.Principal == "" || req.Actor == "" {
		return MigrationResult{}, errors.New("legacy migration requires broker-bound principal and actor")
	}
	if err := req.Identity.validate(); err != nil {
		return MigrationResult{}, err
	}
	archiveSum := sha256.Sum256(req.RawLog)
	archiveDigest := "sha256:" + hex.EncodeToString(archiveSum[:])
	if req.ArchiveDigest != archiveDigest {
		return MigrationResult{}, errors.New("legacy migration archive digest does not match the fixed source bytes")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok, err := completedLegacyMigrationLocked(s.wal.Records(), req.Identity); ok || err != nil {
		return prior, err
	}

	jsonl, quarantined := decodeLegacyMemoryLog(req.RawLog)
	walHeads, walQuarantine, err := decodeLegacyArtifactHeads(s.wal.Records())
	if err != nil {
		return MigrationResult{}, err
	}
	quarantined = append(quarantined, walQuarantine...)
	all := append(jsonl, walHeads...)
	type convertedLegacy struct {
		artifact Artifact
		source   string
	}
	converted := make(map[string]convertedLegacy, len(all))
	for _, record := range all {
		item, convertErr := s.convertLegacyRecord(record, req)
		if convertErr != nil {
			quarantined = append(quarantined, record.source+":"+record.item.ID+":"+convertErr.Error())
			continue
		}
		if prior, exists := converted[item.ID]; exists {
			if !sameLegacyArtifact(prior.artifact, item) {
				quarantined = append(quarantined, "divergent-duplicate:"+item.ID)
			} else if record.source == "wal" {
				// The canonical pre-EP63 WAL head wins only after semantic
				// equivalence is proved, preserving its version and audit fields.
				converted[item.ID] = convertedLegacy{artifact: item, source: record.source}
			}
			continue
		}
		converted[item.ID] = convertedLegacy{artifact: item, source: record.source}
	}
	current, foldErr := fold(s.wal.Records())
	if foldErr != nil {
		return MigrationResult{}, foldErr
	}
	for id, candidate := range converted {
		existing, exists := current[id]
		if candidate.source == "wal" {
			if !exists || !isRetiredLearningKind(existing.Kind) {
				quarantined = append(quarantined, "missing-expected-wal-head:"+id)
			}
			continue
		}
		if exists {
			quarantined = append(quarantined, "destination-id-conflict:"+id)
		}
	}
	sort.Strings(quarantined)
	sourceDigest, err := legacySourceDigest(req.RawLog, walHeads)
	if err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{ArchiveDigest: archiveDigest, SourceDigest: sourceDigest, Identity: req.Identity}
	if len(quarantined) != 0 {
		result.Quarantined = quarantined
		return result, fmt.Errorf("legacy migration quarantined %d record(s); canonical state is unchanged", len(quarantined))
	}

	ids := make([]string, 0, len(converted))
	for id := range converted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	replacements := make([]migrationReplaceEvent, 0, len(ids))
	for _, id := range ids {
		candidate := converted[id]
		replacement := migrationReplaceEvent{Artifact: candidate.artifact, ExpectedAbsent: candidate.source != "wal"}
		if candidate.source == "wal" {
			existing := current[id]
			replacement.Expected = legacyArtifactExpectation(existing)
		}
		replacements = append(replacements, replacement)
	}
	result.Migrated, result.Complete = len(ids), true
	stages, err := buildMigrationStages(result, replacements)
	if err != nil {
		return MigrationResult{}, err
	}
	if err := validateExistingMigrationStageDebt(s.wal.Records(), result, stages, false); err != nil {
		return MigrationResult{}, err
	}
	stageRefs := make([]migrationStageRef, 0, len(stages))
	for _, stage := range stages {
		payload, marshalErr := json.Marshal(stage)
		if marshalErr != nil {
			return MigrationResult{}, marshalErr
		}
		key := legacyStageIdempotencyKey(result, stage.Index)
		tx := wal.Transaction{ID: "legacy-stage-" + strings.TrimPrefix(stage.Digest, "sha256:"), IdempotencyKey: key,
			Principal: req.Principal, Actor: req.Actor, Events: []wal.Event{{Store: artifactStore, Type: legacyMigrationStage, Data: payload}}}
		if _, appendErr := s.wal.Append(tx); appendErr != nil {
			return MigrationResult{}, appendErr
		}
		stageRefs = append(stageRefs, migrationStageRef{Index: stage.Index, Digest: stage.Digest, Items: len(stage.Items)})
	}
	if err := validateExistingMigrationStageDebt(s.wal.Records(), result, stages, true); err != nil {
		return MigrationResult{}, err
	}
	if req.ValidateSource != nil {
		if err := req.ValidateSource(); err != nil {
			return MigrationResult{}, fmt.Errorf("legacy migration source changed before completion: %w", err)
		}
	}
	marker, err := json.Marshal(migrationCompleted{Converter: LegacyConverterVersion, Result: result, Stages: stageRefs})
	if err != nil {
		return MigrationResult{}, err
	}
	events := append([]wal.Event{}, s.kindRegistrationEvents(req.Identity.Memory)...)
	events = append(events, s.kindRegistrationEvents(req.Identity.Lesson)...)
	events = append(events, wal.Event{Store: artifactStore, Type: legacyCompletedEvent, Data: marker})
	idem := "legacy-memory-v1:complete:" + req.Identity.Runtime.ManifestDigest + ":" + sourceDigest
	txID := sha256.Sum256([]byte(idem))
	_, err = s.wal.Append(wal.Transaction{ID: "legacy-complete-" + hex.EncodeToString(txID[:]), IdempotencyKey: idem, Principal: req.Principal, Actor: req.Actor, Events: events})
	return result, err
}

func completedLegacyMigrationLocked(records []wal.Record, identity LegacyMigrationIdentity) (MigrationResult, bool, error) {
	var found *MigrationResult
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != artifactStore || event.Type != legacyCompletedEvent {
				continue
			}
			var marker migrationCompleted
			if err := strictLegacyJSON(event.Data, &marker); err != nil || marker.Converter != LegacyConverterVersion || !marker.Result.Complete {
				return MigrationResult{}, false, errors.New("invalid completed legacy migration marker")
			}
			if found != nil {
				return MigrationResult{}, false, errors.New("multiple completed legacy migration markers")
			}
			copy := marker.Result
			found = &copy
		}
	}
	if found == nil {
		return MigrationResult{}, false, nil
	}
	if !sameMigrationIdentity(found.Identity, identity) {
		return MigrationResult{}, false, errors.New("legacy migration is permanently fenced to a different signed application identity")
	}
	if _, err := fold(records); err != nil {
		return MigrationResult{}, false, fmt.Errorf("completed legacy migration stage set: %w", err)
	}
	return *found, true, nil
}

func sameMigrationIdentity(left, right LegacyMigrationIdentity) bool {
	return left.Runtime == right.Runtime && left.Memory.Schema == right.Memory.Schema && left.Lesson.Schema == right.Lesson.Schema &&
		left.Memory.Kind == right.Memory.Kind && left.Lesson.Kind == right.Lesson.Kind &&
		left.Memory.Definition.SchemaDigest() == right.Memory.Definition.SchemaDigest() &&
		left.Lesson.Definition.SchemaDigest() == right.Lesson.Definition.SchemaDigest()
}

type legacyMemoryItem struct {
	ID          string         `json:"id"`
	MemoryKind  string         `json:"memory_kind,omitempty"`
	Scope       string         `json:"scope"`
	RepoID      string         `json:"repo_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	Kind        string         `json:"kind"`
	Summary     string         `json:"summary"`
	Body        string         `json:"body,omitempty"`
	Lesson      string         `json:"lesson,omitempty"`
	Trigger     string         `json:"trigger,omitempty"`
	Rationale   string         `json:"rationale,omitempty"`
	Evidence    legacyEvidence `json:"evidence,omitempty"`
	Source      legacySource   `json:"source,omitempty"`
	Confidence  string         `json:"confidence"`
	Sensitivity string         `json:"sensitivity"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	ExpiresAt   time.Time      `json:"expires_at,omitempty"`
	Supersedes  []string       `json:"supersedes,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
}

type legacyEvidence struct {
	SessionID string   `json:"session_id,omitempty"`
	Turns     []int    `json:"turns,omitempty"`
	Commits   []string `json:"commits,omitempty"`
	Tests     []string `json:"tests,omitempty"`
	Files     []string `json:"files,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

type legacySource struct {
	SessionID string `json:"session_id,omitempty"`
	Turn      int    `json:"turn,omitempty"`
	Commit    string `json:"commit,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

type legacyMemoryEvent struct {
	Type      string            `json:"type"`
	Action    string            `json:"action"`
	ID        string            `json:"id,omitempty"`
	Actor     string            `json:"actor,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Item      *legacyMemoryItem `json:"item,omitempty"`
}

type legacyRecord struct {
	item       legacyMemoryItem
	legacyID   string
	version    uint64
	authority  Authority
	groups     []string
	provenance Provenance
	evidence   []string
	source     string
}

type migrationExpectation struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Kind    Kind   `json:"kind"`
	Digest  string `json:"digest"`
}

type migrationReplaceEvent struct {
	Artifact       Artifact             `json:"artifact"`
	ExpectedAbsent bool                 `json:"expected_absent,omitempty"`
	Expected       migrationExpectation `json:"expected,omitempty"`
}

type migrationStage struct {
	Converter     int                     `json:"converter_version"`
	Index         int                     `json:"index"`
	SourceDigest  string                  `json:"source_digest"`
	ArchiveDigest string                  `json:"archive_digest"`
	Identity      LegacyMigrationIdentity `json:"identity"`
	Items         []migrationReplaceEvent `json:"items"`
	Digest        string                  `json:"digest"`
}

type migrationStageRef struct {
	Index  int    `json:"index"`
	Digest string `json:"digest"`
	Items  int    `json:"items"`
}

func buildMigrationStages(result MigrationResult, replacements []migrationReplaceEvent) ([]migrationStage, error) {
	if len(replacements) == 0 {
		return nil, nil
	}
	var stages []migrationStage
	for len(replacements) > 0 {
		stage := migrationStage{Converter: LegacyConverterVersion, Index: len(stages), SourceDigest: result.SourceDigest,
			ArchiveDigest: result.ArchiveDigest, Identity: result.Identity}
		for len(replacements) > 0 {
			candidate := append(append([]migrationReplaceEvent(nil), stage.Items...), replacements[0])
			probe := stage
			probe.Items = candidate
			encoded, err := json.Marshal(probe)
			if err != nil {
				return nil, err
			}
			if len(encoded) > maxLegacyStageBytes && len(stage.Items) > 0 {
				break
			}
			if len(encoded) > maxLegacyStageBytes {
				return nil, errors.New("one legacy artifact exceeds the bounded migration stage")
			}
			stage.Items = candidate
			replacements = replacements[1:]
		}
		digest, err := migrationStageDigest(stage)
		if err != nil {
			return nil, err
		}
		stage.Digest = digest
		stages = append(stages, stage)
	}
	return stages, nil
}

func migrationStageDigest(stage migrationStage) (string, error) {
	stage.Digest = ""
	raw, err := json.Marshal(stage)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func legacyStageIdempotencyKey(result MigrationResult, index int) string {
	return fmt.Sprintf("legacy-memory-v1:stage:%s:%s:%d", result.Identity.Runtime.ManifestDigest, result.SourceDigest, index)
}

func validateExistingMigrationStageDebt(records []wal.Record, result MigrationResult, desired []migrationStage, requireComplete bool) error {
	seen := map[int]string{}
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != artifactStore || event.Type != legacyMigrationStage {
				continue
			}
			var stage migrationStage
			if err := strictLegacyJSON(event.Data, &stage); err != nil {
				return fmt.Errorf("corrupt legacy migration stage at sequence %d: %w", record.Sequence, err)
			}
			if stage.Converter != LegacyConverterVersion || !sameMigrationIdentity(stage.Identity, result.Identity) ||
				stage.SourceDigest != result.SourceDigest || stage.ArchiveDigest != result.ArchiveDigest {
				return errors.New("legacy migration staging debt belongs to different source bytes or identity")
			}
			digest, err := migrationStageDigest(stage)
			if err != nil || digest != stage.Digest || len(stage.Items) == 0 || stage.Index < 0 {
				return errors.New("legacy migration stage digest or envelope is invalid")
			}
			if _, duplicate := seen[stage.Index]; duplicate {
				return errors.New("duplicate legacy migration stage index")
			}
			if stage.Index >= len(desired) || stage.Digest != desired[stage.Index].Digest || len(stage.Items) != len(desired[stage.Index].Items) {
				return errors.New("legacy migration staging debt differs from the freshly recomputed stage set")
			}
			seen[stage.Index] = stage.Digest
		}
	}
	for index := 0; index < len(seen); index++ {
		if _, ok := seen[index]; !ok {
			return errors.New("legacy migration staging debt is not a contiguous prefix")
		}
	}
	if requireComplete && len(seen) != len(desired) {
		return errors.New("legacy migration completion is missing a recomputed stage")
	}
	return nil
}

func validateMigrationStage(stage migrationStage) error {
	if stage.Converter != LegacyConverterVersion || stage.Index < 0 || len(stage.Items) == 0 ||
		stage.SourceDigest == "" || stage.ArchiveDigest == "" {
		return errors.New("invalid legacy migration stage envelope")
	}
	if err := stage.Identity.validate(); err != nil {
		return err
	}
	digest, err := migrationStageDigest(stage)
	if err != nil || digest != stage.Digest {
		return errors.New("invalid legacy migration stage digest")
	}
	return nil
}

func applyCompletedMigration(current map[string]Artifact, stages map[int]migrationStage, marker migrationCompleted) (map[string]Artifact, error) {
	if marker.Converter != LegacyConverterVersion || !marker.Result.Complete || marker.Result.Migrated < 0 {
		return nil, errors.New("invalid completed legacy migration marker")
	}
	if err := marker.Result.Identity.validate(); err != nil {
		return nil, err
	}
	if len(marker.Stages) != len(stages) {
		return nil, errors.New("completed legacy migration has missing or extra stages")
	}
	projected := make(map[string]Artifact, len(current)+marker.Result.Migrated)
	for id, item := range current {
		projected[id] = item
	}
	seenIDs := map[string]bool{}
	total := 0
	for index, ref := range marker.Stages {
		if ref.Index != index || ref.Items <= 0 {
			return nil, errors.New("completed legacy migration stage order is invalid")
		}
		stage, ok := stages[index]
		if !ok || stage.Digest != ref.Digest || len(stage.Items) != ref.Items ||
			stage.SourceDigest != marker.Result.SourceDigest || stage.ArchiveDigest != marker.Result.ArchiveDigest ||
			!sameMigrationIdentity(stage.Identity, marker.Result.Identity) {
			return nil, errors.New("completed legacy migration stage binding diverged")
		}
		for _, replacement := range stage.Items {
			if replacement.Artifact.ID == "" || replacement.ExpectedAbsent == (replacement.Expected.ID != "") || seenIDs[replacement.Artifact.ID] {
				return nil, errors.New("invalid or duplicate legacy migration replacement")
			}
			seenIDs[replacement.Artifact.ID] = true
			existing, exists := projected[replacement.Artifact.ID]
			if replacement.ExpectedAbsent {
				if exists {
					return nil, errors.New("legacy migration expected an absent destination id")
				}
			} else if !exists || legacyArtifactExpectation(existing) != replacement.Expected {
				return nil, errors.New("legacy migration source expectation diverged")
			}
			projected[replacement.Artifact.ID] = replacement.Artifact
			total++
		}
	}
	if total != marker.Result.Migrated {
		return nil, errors.New("completed legacy migration item count diverged")
	}
	return projected, nil
}

func legacyArtifactExpectation(item Artifact) migrationExpectation {
	raw, _ := json.Marshal(item)
	sum := sha256.Sum256(raw)
	return migrationExpectation{ID: item.ID, Version: item.Version, Kind: item.Kind, Digest: "sha256:" + hex.EncodeToString(sum[:])}
}

func isRetiredLearningKind(kind Kind) bool {
	switch kind {
	case "memory", "lesson", "stado.dev/bundled/learn#memory", "stado.dev/bundled/learn#lesson":
		return true
	default:
		return false
	}
}

func decodeLegacyMemoryLog(raw []byte) ([]legacyRecord, []string) {
	items := make(map[string]legacyMemoryItem)
	var quarantined []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64<<10), maxLegacyEventBytes)
	line := 0
	for scanner.Scan() {
		line++
		body := bytes.TrimSpace(scanner.Bytes())
		if len(body) == 0 {
			continue
		}
		var event legacyMemoryEvent
		if err := strictLegacyJSON(body, &event); err != nil {
			quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:malformed", line))
			continue
		}
		if event.Type != "memory" {
			quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:unexpected-type", line))
			continue
		}
		id := event.ID
		if event.Action != "supersede" && event.Item != nil && event.Item.ID != "" {
			id = event.Item.ID
		}
		if id == "" {
			quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:missing-id", line))
			continue
		}
		switch event.Action {
		case "propose", "upsert", "edit":
			if event.Item == nil {
				quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:missing-item", line))
				continue
			}
			items[id] = *event.Item
		case "approve", "reject", "delete":
			item, ok := items[id]
			if !ok {
				quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:unbound-transition", line))
				continue
			}
			item.Confidence = map[string]string{"approve": "approved", "reject": "rejected", "delete": "deleted"}[event.Action]
			item.UpdatedAt = event.Timestamp
			items[id] = item
		case "supersede":
			if event.Item == nil || event.Item.ID == "" {
				quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:invalid-supersede", line))
				continue
			}
			old, ok := items[id]
			if !ok {
				quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:unbound-supersede", line))
				continue
			}
			old.Confidence, old.UpdatedAt = "superseded", event.Timestamp
			items[id] = old
			items[event.Item.ID] = *event.Item
		default:
			quarantined = append(quarantined, fmt.Sprintf("jsonl:line:%d:unknown-action", line))
		}
	}
	if scanner.Err() != nil {
		quarantined = append(quarantined, "jsonl:scan-failed")
	}
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]legacyRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, legacyRecord{item: items[id], version: 1, source: "jsonl"})
	}
	return out, quarantined
}

type legacyArtifactWire struct {
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
	Provenance   json.RawMessage `json:"provenance"`
	Data         json.RawMessage `json:"data"`
	Summary      string          `json:"summary"`
	Content      string          `json:"content"`
	Trigger      string          `json:"trigger"`
	Expected     string          `json:"expected_outcome"`
	Validation   string          `json:"validation"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ExpiresAt    time.Time       `json:"expires_at,omitempty"`
	Supersedes   []string        `json:"supersedes,omitempty"`
	LegacyID     string          `json:"legacy_id,omitempty"`
}

type legacyReplaceWire struct {
	ID              string          `json:"id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Artifact        json.RawMessage `json:"artifact"`
}

func decodeLegacyArtifactHeads(records []wal.Record) ([]legacyRecord, []string, error) {
	heads := make(map[string]legacyRecord)
	var quarantined []string
	for _, record := range records {
		for _, event := range record.Transaction.Events {
			if event.Store != artifactStore {
				continue
			}
			switch event.Type {
			case "artifact.create":
				var envelope struct {
					Artifact json.RawMessage `json:"artifact"`
				}
				if err := strictLegacyJSON(event.Data, &envelope); err != nil {
					return nil, nil, fmt.Errorf("legacy artifact create at sequence %d: %w", record.Sequence, err)
				}
				legacy, ok, err := decodeLegacyArtifact(envelope.Artifact)
				if err != nil {
					quarantined = append(quarantined, fmt.Sprintf("wal:sequence:%d:invalid-create", record.Sequence))
					continue
				}
				if ok {
					if _, duplicate := heads[legacy.item.ID]; duplicate {
						quarantined = append(quarantined, fmt.Sprintf("wal:sequence:%d:duplicate-create:%s", record.Sequence, legacy.item.ID))
						continue
					}
					heads[legacy.item.ID] = legacy
				}
			case "artifact.edit":
				var replacement legacyReplaceWire
				if err := strictLegacyJSON(event.Data, &replacement); err != nil {
					return nil, nil, fmt.Errorf("legacy artifact edit at sequence %d: %w", record.Sequence, err)
				}
				legacy, ok, err := decodeLegacyArtifact(replacement.Artifact)
				if err != nil {
					quarantined = append(quarantined, fmt.Sprintf("wal:sequence:%d:invalid-edit", record.Sequence))
					continue
				}
				if !ok {
					continue
				}
				prior, exists := heads[replacement.ID]
				if !exists || prior.version != replacement.ExpectedVersion || legacy.item.ID != replacement.ID {
					quarantined = append(quarantined, fmt.Sprintf("wal:sequence:%d:unbound-edit", record.Sequence))
					continue
				}
				heads[replacement.ID] = legacy
			case "artifact.authority":
				var authority authorityEvent
				if err := strictLegacyJSON(event.Data, &authority); err != nil {
					return nil, nil, fmt.Errorf("legacy artifact authority at sequence %d: %w", record.Sequence, err)
				}
				prior, exists := heads[authority.ID]
				if !exists {
					continue
				}
				if prior.version != authority.ExpectedVersion {
					quarantined = append(quarantined, fmt.Sprintf("wal:sequence:%d:unbound-authority", record.Sequence))
					continue
				}
				prior.version++
				prior.authority = authority.Authority
				prior.item.UpdatedAt = authority.UpdatedAt
				heads[authority.ID] = prior
			}
		}
	}
	ids := make([]string, 0, len(heads))
	for id := range heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]legacyRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, heads[id])
	}
	return out, quarantined, nil
}

func decodeLegacyArtifact(raw json.RawMessage) (legacyRecord, bool, error) {
	var peek struct {
		Kind Kind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return legacyRecord{}, false, err
	}
	if !isRetiredLearningKind(peek.Kind) {
		return legacyRecord{}, false, nil
	}
	var wire legacyArtifactWire
	if err := strictLegacyJSON(raw, &wire); err != nil {
		return legacyRecord{}, false, err
	}
	local := ""
	switch string(wire.Kind) {
	case "memory", "stado.dev/bundled/learn#memory":
		local = "memory"
	case "lesson", "stado.dev/bundled/learn#lesson":
		local = "lesson"
	default:
		return legacyRecord{}, false, nil
	}
	var data struct {
		Summary         string `json:"summary"`
		Content         string `json:"content"`
		Trigger         string `json:"trigger"`
		ExpectedOutcome string `json:"expected_outcome"`
		Validation      string `json:"validation"`
	}
	if len(wire.Data) != 0 {
		if err := strictLegacyJSON(wire.Data, &data); err != nil {
			return legacyRecord{}, true, err
		}
	} else {
		data.Summary, data.Content, data.Trigger, data.ExpectedOutcome, data.Validation = wire.Summary, wire.Content, wire.Trigger, wire.Expected, wire.Validation
	}
	item := legacyMemoryItem{
		ID: wire.ID, MemoryKind: local, Scope: string(wire.Scope), RepoID: wire.Binding.CanonicalRepoID,
		SessionID: wire.Binding.AnchorSessionID, Summary: data.Summary, Body: data.Content, Trigger: data.Trigger,
		Rationale: data.ExpectedOutcome, Confidence: string(wire.Authority), Sensitivity: wire.Sensitivity,
		CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt, ExpiresAt: wire.ExpiresAt,
		Supersedes: append([]string(nil), wire.Supersedes...), Tags: append([]string(nil), wire.Tags...),
	}
	if local == "lesson" {
		item.Lesson = data.Content
	}
	provenance, err := decodeLegacyProvenance(wire.Provenance)
	if err != nil {
		return legacyRecord{}, true, err
	}
	appendLegacyIDRef(&provenance, wire.LegacyID)
	return legacyRecord{item: item, legacyID: wire.LegacyID, version: wire.Version, authority: wire.Authority, groups: wire.Groups,
		provenance: provenance, evidence: wire.EvidenceRefs, source: "wal"}, true, nil
}

func decodeLegacyProvenance(raw json.RawMessage) (Provenance, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Provenance{}, nil
	}
	var object Provenance
	if err := strictLegacyJSON(raw, &object); err == nil {
		return object, nil
	}
	var origins []string
	if err := strictLegacyJSON(raw, &origins); err != nil {
		return Provenance{}, err
	}
	return Provenance{Origins: origins}, nil
}

// decodeArtifactCreateEvent and decodeArtifactEditEvent keep retired shapes
// inside this migration-only reader. Generic Artifact JSON has no legacy
// aliases or fabricated kind identity.
func decodeArtifactCreateEvent(raw json.RawMessage) (createEvent, error) {
	var envelope struct {
		Artifact json.RawMessage `json:"artifact"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return createEvent{}, err
	}
	artifact, err := decodeArtifactForFold(envelope.Artifact)
	return createEvent{Artifact: artifact}, err
}

func decodeArtifactEditEvent(raw json.RawMessage) (replaceEvent, error) {
	var envelope struct {
		ID              string          `json:"id"`
		ExpectedVersion uint64          `json:"expected_version"`
		Artifact        json.RawMessage `json:"artifact"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return replaceEvent{}, err
	}
	artifact, err := decodeArtifactForFold(envelope.Artifact)
	return replaceEvent{ID: envelope.ID, ExpectedVersion: envelope.ExpectedVersion, Artifact: artifact}, err
}

func decodeArtifactForFold(raw json.RawMessage) (Artifact, error) {
	var peek struct {
		Kind Kind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return Artifact{}, err
	}
	if !isRetiredLearningKind(peek.Kind) {
		var artifact Artifact
		return artifact, json.Unmarshal(raw, &artifact)
	}
	var wire legacyArtifactWire
	if err := strictLegacyJSON(raw, &wire); err != nil {
		return Artifact{}, err
	}
	provenance, err := decodeLegacyProvenance(wire.Provenance)
	if err != nil {
		return Artifact{}, err
	}
	appendLegacyIDRef(&provenance, wire.LegacyID)
	data := append(json.RawMessage(nil), wire.Data...)
	if len(data) == 0 {
		fields := map[string]string{"summary": wire.Summary}
		for key, value := range map[string]string{"content": wire.Content, "trigger": wire.Trigger, "expected_outcome": wire.Expected, "validation": wire.Validation} {
			if value != "" {
				fields[key] = value
			}
		}
		data, err = json.Marshal(fields)
		if err != nil {
			return Artifact{}, err
		}
	}
	return Artifact{
		APIVersion: wire.APIVersion, ID: wire.ID, Version: wire.Version, Kind: wire.Kind, KindSchema: wire.KindSchema,
		Scope: wire.Scope, Binding: wire.Binding, Authority: wire.Authority, Tags: wire.Tags, Groups: wire.Groups,
		EvidenceRefs: wire.EvidenceRefs, Sensitivity: wire.Sensitivity, Provenance: provenance, Data: data,
		CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt, ExpiresAt: wire.ExpiresAt, Supersedes: wire.Supersedes,
	}, nil
}

func (s *Service) convertLegacyRecord(record legacyRecord, req LegacyMigration) (Artifact, error) {
	old := record.item
	if old.ID == "" || record.version == 0 {
		return Artifact{}, errors.New("missing id or version")
	}
	if old.CreatedAt.IsZero() || old.UpdatedAt.IsZero() || old.UpdatedAt.Before(old.CreatedAt) {
		return Artifact{}, errors.New("missing or invalid historical timestamps")
	}
	local := "memory"
	descriptor := req.Identity.Memory
	if strings.EqualFold(strings.TrimSpace(old.MemoryKind), "lesson") {
		local, descriptor = "lesson", req.Identity.Lesson
	}
	data := map[string]string{"summary": old.Summary}
	if local == "lesson" {
		data["content"], data["trigger"], data["expected_outcome"] = firstNonEmpty(old.Lesson, old.Body), old.Trigger, old.Rationale
	} else {
		data["content"], data["trigger"] = old.Body, old.Trigger
	}
	for key, value := range data {
		if value == "" && key != "summary" {
			delete(data, key)
		}
	}
	rawData, err := json.Marshal(data)
	if err != nil {
		return Artifact{}, err
	}
	item := Artifact{
		APIVersion: APIVersionV1, ID: old.ID, Version: record.version, Kind: descriptor.Kind, KindSchema: descriptor.Schema,
		Scope: Scope(old.Scope), Tags: append([]string(nil), old.Tags...), Groups: append([]string(nil), record.groups...),
		EvidenceRefs: append([]string(nil), record.evidence...), Sensitivity: old.Sensitivity,
		Provenance: record.provenance, Data: rawData, CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt,
		ExpiresAt: old.ExpiresAt, Supersedes: append([]string(nil), old.Supersedes...),
	}
	appendLegacyIDRef(&item.Provenance, firstNonEmpty(record.legacyID, old.ID))
	item.Binding.Principal = req.Principal
	switch item.Scope {
	case ScopeGlobal:
	case ScopeRepo:
		item.Binding.CanonicalRepoID = strings.TrimSpace(old.RepoID)
	case ScopeSession:
		if req.ResolveSessionAnchor == nil {
			return Artifact{}, errors.New("session anchor resolver unavailable")
		}
		anchor, ok := req.ResolveSessionAnchor(strings.TrimSpace(old.SessionID))
		if !ok || anchor == "" {
			return Artifact{}, errors.New("unresolved durable session anchor")
		}
		item.Binding.AnchorSessionID = anchor
	default:
		return Artifact{}, errors.New("invalid scope")
	}
	if item.Provenance.CreatedBy == "" {
		item.Provenance.CreatedBy = old.Source.CreatedBy
	}
	item.Provenance.Origins = append(item.Provenance.Origins, "legacy:"+record.source)
	appendRef := func(prefix, value string) {
		if strings.TrimSpace(value) != "" {
			item.EvidenceRefs = append(item.EvidenceRefs, prefix+value)
		}
	}
	appendRef("session:", old.Evidence.SessionID)
	for _, turn := range old.Evidence.Turns {
		appendRef("turn:", strconv.Itoa(turn))
	}
	for _, commit := range old.Evidence.Commits {
		appendRef("commit:", commit)
	}
	for _, test := range old.Evidence.Tests {
		appendRef("test:", test)
	}
	for _, file := range old.Evidence.Files {
		appendRef("file:", file)
	}
	appendRef("note:", old.Evidence.Notes)
	appendRef("source-session:", old.Source.SessionID)
	if old.Source.Turn > 0 {
		appendRef("source-turn:", strconv.Itoa(old.Source.Turn))
	}
	appendRef("source-commit:", old.Source.Commit)
	if _, err := s.prepare(&item, req.Principal); err != nil {
		return Artifact{}, err
	}
	if record.authority != "" {
		item.Authority, err = migratedAuthority(string(record.authority))
	} else {
		item.Authority, err = migratedAuthority(old.Confidence)
	}
	if err != nil {
		return Artifact{}, err
	}
	return item, nil
}

func migratedAuthority(value string) (Authority, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approved", "active", "legacy_active":
		return AuthorityActive, nil
	case "candidate", "":
		return AuthorityCandidate, nil
	case "rejected":
		return AuthorityRejected, nil
	case "superseded":
		return AuthoritySuperseded, nil
	case "retired":
		return AuthorityRetired, nil
	case "deleted":
		return AuthorityDeleted, nil
	default:
		return "", fmt.Errorf("invalid authority %q", value)
	}
}

func sameLegacyArtifact(left, right Artifact) bool {
	left.Provenance, right.Provenance = Provenance{}, Provenance{}
	left.EvidenceRefs, right.EvidenceRefs = nil, nil
	left.Groups, right.Groups = nil, nil
	left.Version, right.Version = 0, 0
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return bytes.Equal(leftRaw, rightRaw)
}

func legacySourceDigest(raw []byte, walHeads []legacyRecord) (string, error) {
	hash := sha256.New()
	_, _ = hash.Write([]byte("legacy-memory-v1\x00jsonl\x00"))
	_, _ = hash.Write(raw)
	_, _ = hash.Write([]byte("\x00wal-heads\x00"))
	for _, head := range walHeads {
		watermark := struct {
			Item       legacyMemoryItem `json:"item"`
			LegacyID   string           `json:"legacy_id,omitempty"`
			Version    uint64           `json:"version"`
			Authority  Authority        `json:"authority"`
			Groups     []string         `json:"groups,omitempty"`
			Provenance Provenance       `json:"provenance"`
			Evidence   []string         `json:"evidence,omitempty"`
			Source     string           `json:"source"`
		}{head.item, head.legacyID, head.version, head.authority, head.groups, head.provenance, head.evidence, head.source}
		encoded, err := json.Marshal(watermark)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func appendLegacyIDRef(provenance *Provenance, id string) {
	if provenance == nil || strings.TrimSpace(id) == "" {
		return
	}
	ref := "legacy-id:" + id
	for _, existing := range provenance.Refs {
		if existing == ref {
			return
		}
	}
	provenance.Refs = append(provenance.Refs, ref)
}

func strictLegacyJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
