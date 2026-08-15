package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

type failCompletionAppender struct {
	Appender
	fail bool
}

func (a *failCompletionAppender) Append(transaction wal.Transaction) (wal.AppendResult, error) {
	if a.fail {
		for _, event := range transaction.Events {
			if event.Store == artifactStore && event.Type == legacyCompletedEvent {
				a.fail = false
				return wal.AppendResult{}, errors.New("injected completion failure")
			}
		}
	}
	return a.Appender.Append(transaction)
}

func migrationFixture(t *testing.T) (*Service, *wal.Store, LegacyMigrationIdentity) {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, grants := authority.New(store)
	definitions := []plugins.ArtifactKindDef{
		{Name: "memory", Schema: `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"},"trigger":{"type":"string"}}}`, Index: []plugins.ArtifactIndexProjection{{Pointer: "/summary", Role: "title"}, {Pointer: "/content", Role: "text"}, {Pointer: "/trigger", Role: "trigger"}}},
		{Name: "lesson", Schema: `{"type":"object","additionalProperties":false,"required":["summary","trigger"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"},"trigger":{"type":"string","minLength":1},"expected_outcome":{"type":"string"}}}`, Index: []plugins.ArtifactIndexProjection{{Pointer: "/summary", Role: "title"}, {Pointer: "/content", Role: "text"}, {Pointer: "/trigger", Role: "trigger"}}},
	}
	manifest := plugins.Manifest{Name: "memory", Version: "v1.0.0", ArtifactKinds: definitions}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewKindRegistry()
	if err := registry.Register(identity, definitions); err != nil {
		t.Fatal(err)
	}
	memoryKind, _ := identity.QualifiedKind("memory")
	lessonKind, _ := identity.QualifiedKind("lesson")
	memoryDescriptor, _ := registry.Lookup(Kind(memoryKind))
	lessonDescriptor, _ := registry.Lookup(Kind(lessonKind))
	return NewServiceWithKinds(store, grants, registry), store, LegacyMigrationIdentity{Runtime: identity, Memory: memoryDescriptor, Lesson: lessonDescriptor}
}

func legacyLog(t *testing.T, items ...legacyMemoryItem) []byte {
	t.Helper()
	var out strings.Builder
	for _, item := range items {
		raw, err := json.Marshal(legacyMemoryEvent{Type: "memory", Action: "upsert", ID: item.ID, Actor: "legacy", Timestamp: item.UpdatedAt, Item: &item})
		if err != nil {
			t.Fatal(err)
		}
		out.Write(raw)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func archiveDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestLegacyMigrationPreservesStatusScopeSecretAndPermanentFence(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t,
		legacyMemoryItem{ID: "mem_old", Scope: "repo", RepoID: "repo-1", Summary: "ordinary", Confidence: "approved", Sensitivity: "secret", CreatedAt: now, UpdatedAt: now},
		legacyMemoryItem{ID: "lesson_old", MemoryKind: "lesson", Scope: "session", SessionID: "logical-session-1", Summary: "lesson", Lesson: "do this", Trigger: "when x", Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now},
	)
	req := LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin:" + identity.Runtime.Canonical, Identity: identity,
		ResolveSessionAnchor: func(subject string) (string, bool) { return "broker-session-1", subject == "logical-session-1" }}
	result, err := svc.MigrateLegacy(context.Background(), req)
	if err != nil || !result.Complete || result.Migrated != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	memory, ok, err := svc.Show("mem_old")
	if err != nil || !ok || memory.Authority != AuthorityActive || memory.Sensitivity != "secret" || memory.Binding.CanonicalRepoID != "repo-1" ||
		!slices.Contains(memory.Provenance.Refs, "legacy-id:mem_old") {
		t.Fatalf("memory=%+v ok=%v err=%v", memory, ok, err)
	}
	lesson, ok, err := svc.Show("lesson_old")
	if err != nil || !ok || lesson.Authority != AuthorityActive || lesson.Binding.AnchorSessionID != "broker-session-1" {
		t.Fatalf("lesson=%+v ok=%v err=%v", lesson, ok, err)
	}
	before := len(store.Records())
	prior, found, err := svc.CompletedLegacyMigration(identity)
	if err != nil || !found || prior.SourceDigest != result.SourceDigest {
		t.Fatalf("prior=%+v found=%v err=%v", prior, found, err)
	}
	// The completed marker fences source rereads: the exact durable result is
	// returned without consulting changed/removed legacy bytes.
	if len(store.Records()) != before {
		t.Fatal("status check appended canonical state")
	}
}

func TestLegacyMigrationQuarantineIsAllOrNothing(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t,
		legacyMemoryItem{ID: "good", Scope: "global", Summary: "good", Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now},
		legacyMemoryItem{ID: "bad", Scope: "session", SessionID: "unknown", Summary: "bad", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now},
	)
	result, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity,
		ResolveSessionAnchor: func(string) (string, bool) { return "", false }})
	if err == nil || len(result.Quarantined) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, ok, showErr := svc.Show("good"); showErr != nil || ok {
		t.Fatalf("partial migration committed ok=%v err=%v", ok, showErr)
	}
	if len(store.Records()) != 0 {
		t.Fatalf("quarantine appended %d WAL records", len(store.Records()))
	}
}

func TestLegacyMigrationIdentityAndArchiveMismatchFailClosed(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	raw := []byte{}
	if _, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: "sha256:" + strings.Repeat("0", 64), Principal: "alice", Actor: "plugin", Identity: identity}); err == nil {
		t.Fatal("archive mismatch accepted")
	}
	identity.Memory.Schema.SchemaDigest = "sha256:" + strings.Repeat("0", 64)
	if _, _, err := svc.CompletedLegacyMigration(identity); err == nil {
		t.Fatal("descriptor digest mismatch accepted")
	}
	if len(store.Records()) != 0 {
		t.Fatal("failed migration appended WAL")
	}
}

func TestStrictLegacyJSONRejectsTrailingSyntax(t *testing.T) {
	var item legacyMemoryItem
	for _, raw := range []string{`{"id":"x"} garbage`, `{"id":"x"}{"id":"y"}`} {
		if err := strictLegacyJSON([]byte(raw), &item); err == nil {
			t.Fatalf("trailing data accepted: %q", raw)
		}
	}
}

func TestLegacySourceDigestBindsCompleteWALHead(t *testing.T) {
	base := legacyRecord{item: legacyMemoryItem{ID: "x"}, version: 2, authority: AuthorityActive, source: "wal"}
	first, err := legacySourceDigest(nil, []legacyRecord{base})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []legacyRecord{base, base, base, base, base, base, base}
	mutations[0].legacyID = "historical-alias"
	mutations[1].version++
	mutations[2].authority = AuthorityRejected
	mutations[3].groups = []string{"g"}
	mutations[4].provenance = Provenance{Origins: []string{"different"}}
	mutations[5].evidence = []string{"commit:different"}
	mutations[6].source = "different"
	for _, mutation := range mutations {
		digest, err := legacySourceDigest(nil, []legacyRecord{mutation})
		if err != nil || digest == first {
			t.Fatalf("watermark mutation not bound: digest=%q err=%v", digest, err)
		}
	}
}

func appendLegacyArtifactHead(t *testing.T, store *wal.Store, item legacyMemoryItem, version uint64, authority Authority, provenance json.RawMessage, evidence []string) {
	t.Helper()
	fields := map[string]string{"summary": item.Summary}
	if item.Body != "" {
		fields["content"] = item.Body
	}
	if item.Trigger != "" {
		fields["trigger"] = item.Trigger
	}
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	wire := legacyArtifactWire{
		ID: item.ID, Version: version, Kind: Kind("stado.dev/bundled/learn#" + item.MemoryKind),
		Scope: Scope(item.Scope), Binding: ScopeBinding{CanonicalRepoID: item.RepoID, AnchorSessionID: item.SessionID},
		Authority: authority, Tags: item.Tags, EvidenceRefs: evidence, Sensitivity: item.Sensitivity,
		Provenance: provenance, Data: data, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if item.MemoryKind == "" {
		wire.Kind = "stado.dev/bundled/learn#memory"
	}
	body, err := json.Marshal(struct {
		Artifact legacyArtifactWire `json:"artifact"`
	}{wire})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(wal.Transaction{ID: "legacy-head-" + item.ID, IdempotencyKey: "legacy-head-" + item.ID, Principal: "alice", Actor: "legacy", Events: []wal.Event{{Store: artifactStore, Type: "artifact.create", Data: body}}}); err != nil {
		t.Fatal(err)
	}
}

func TestEquivalentJSONLAndWALDuplicateRetainsWALAuditHead(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	item := legacyMemoryItem{ID: "same", MemoryKind: "memory", Scope: "global", Summary: "same", Body: "body", Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now}
	appendLegacyArtifactHead(t, store, item, 7, AuthorityActive, json.RawMessage(`{"origins":["wal:audit"],"created_by":"legacy"}`), []string{"commit:wal"})
	raw := legacyLog(t, item)
	result, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity})
	if err != nil || !result.Complete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got, ok, err := svc.Show(item.ID)
	if err != nil || !ok || got.Version != 7 || got.Provenance.CreatedBy != "legacy" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "commit:wal" {
		t.Fatalf("WAL head not retained: got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestMigrationIgnoresUnrelatedNewArtifactShape(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	data := []byte(`{"artifact":{"api_version":"stado.dev/artifact/v1","id":"new","version":1,"kind":"github.com/acme/new#future","future_field":true}}`)
	if _, err := store.Append(wal.Transaction{ID: "unrelated", IdempotencyKey: "unrelated", Principal: "alice", Actor: "new", Events: []wal.Event{{Store: artifactStore, Type: "artifact.create", Data: data}}}); err != nil {
		t.Fatal(err)
	}
	raw := legacyLog(t, legacyMemoryItem{ID: "old", Scope: "global", Summary: "old", Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	_, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRejectsZeroTimestampAndDestinationConflict(t *testing.T) {
	for _, test := range []struct {
		name        string
		destination bool
	}{
		{name: "zero timestamp"},
		{name: "destination conflict", destination: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, store, identity := migrationFixture(t)
			defer store.Close()
			now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
			item := legacyMemoryItem{ID: "collision", Scope: "global", Summary: "old", Confidence: "candidate", Sensitivity: "normal"}
			if test.destination {
				item.CreatedAt, item.UpdatedAt = now, now
				if _, err := svc.Create(context.Background(), Artifact{ID: item.ID, Kind: identity.Memory.Kind, Scope: ScopeGlobal, Data: json.RawMessage(`{"summary":"destination"}`)}, "alice", "native", "destination"); err != nil {
					t.Fatal(err)
				}
			}
			raw := legacyLog(t, item)
			if _, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity}); err == nil {
				t.Fatal("invalid migration accepted")
			}
		})
	}
}

func TestMigrationReplacementCorruptReplayFailsFold(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t, legacyMemoryItem{ID: "old", Scope: "global", Summary: "old", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	result, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := svc.Show("old")
	if err != nil || !ok {
		t.Fatal(err)
	}
	stages, err := buildMigrationStages(result, []migrationReplaceEvent{{Artifact: item, ExpectedAbsent: true}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(stages[0])
	if _, err := store.Append(wal.Transaction{ID: "corrupt-replay", IdempotencyKey: "corrupt-replay", Principal: "alice", Actor: "corrupt", Events: []wal.Event{{Store: artifactStore, Type: legacyMigrationStage, Data: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Show("old"); err == nil {
		t.Fatal("unconditional migration replay overwrite folded successfully")
	}
}

func TestLegacyMigrationStagesStoreLargerThanOneWALRecord(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	items := make([]legacyMemoryItem, 10)
	for i := range items {
		items[i] = legacyMemoryItem{ID: fmt.Sprintf("large-%02d", i), Scope: "global", Summary: fmt.Sprintf("large %d", i),
			Body: strings.Repeat(string(rune('a'+i)), 900<<10), Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now}
	}
	raw := legacyLog(t, items...)
	if len(raw) <= 8<<20 {
		t.Fatalf("fixture is only %d bytes", len(raw))
	}
	result, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity})
	if err != nil || !result.Complete || result.Migrated != len(items) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	stages := 0
	for _, record := range store.Records() {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil || len(encoded) > 8<<20 {
			t.Fatalf("record bytes=%d err=%v", len(encoded), marshalErr)
		}
		for _, event := range record.Transaction.Events {
			if event.Type == legacyMigrationStage {
				stages++
			}
		}
	}
	if stages < 2 {
		t.Fatalf("large source used only %d stage(s)", stages)
	}
	if _, ok, err := svc.Show("large-09"); err != nil || !ok {
		t.Fatalf("completed staged projection missing: ok=%v err=%v", ok, err)
	}
}

func TestPartialMigrationStagesAreInvisibleAndRestartResumes(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t, legacyMemoryItem{ID: "partial", Scope: "global", Summary: "partial", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	req := LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity}
	svc.wal = &failCompletionAppender{Appender: store, fail: true}
	if _, err := svc.MigrateLegacy(context.Background(), req); err == nil {
		t.Fatal("injected completion failure was ignored")
	}
	if _, ok, err := svc.Show("partial"); err != nil || ok {
		t.Fatalf("partial stage became visible: ok=%v err=%v", ok, err)
	}
	before := len(store.Records())
	restarted := NewServiceWithKinds(store, svc.grants, svc.kinds)
	result, err := restarted.MigrateLegacy(context.Background(), req)
	if err != nil || !result.Complete {
		t.Fatalf("restart result=%+v err=%v", result, err)
	}
	// Retry reuses the exact stage and appends only the completion transaction.
	if got := len(store.Records()); got != before+1 {
		t.Fatalf("restart appended %d records, want one completion", got-before)
	}
	if _, ok, err := restarted.Show("partial"); err != nil || !ok {
		t.Fatalf("completed restart missing: ok=%v err=%v", ok, err)
	}
}

func TestCorruptOrChangedPartialStageFailsClosed(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t, legacyMemoryItem{ID: "partial", Scope: "global", Summary: "partial", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	req := LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity}
	svc.wal = &failCompletionAppender{Appender: store, fail: true}
	if _, err := svc.MigrateLegacy(context.Background(), req); err == nil {
		t.Fatal("injected completion failure was ignored")
	}
	svc.wal = store
	changed := legacyLog(t, legacyMemoryItem{ID: "changed", Scope: "global", Summary: "changed", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	if _, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: changed, ArchiveDigest: archiveDigest(changed), Principal: "alice", Actor: "plugin", Identity: identity}); err == nil {
		t.Fatal("changed source accepted over partial staging debt")
	}
	var stage migrationStage
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Type == legacyMigrationStage {
				if err := json.Unmarshal(event.Data, &stage); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	stage.Digest = "sha256:" + strings.Repeat("0", 64)
	payload, _ := json.Marshal(stage)
	if _, err := store.Append(wal.Transaction{ID: "corrupt-stage", IdempotencyKey: "corrupt-stage", Principal: "alice", Actor: "corrupt",
		Events: []wal.Event{{Store: artifactStore, Type: legacyMigrationStage, Data: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Show("partial"); err == nil {
		t.Fatal("corrupt stage did not fail the projection closed")
	}
}

func TestValidExtraStageDebtCannotPoisonCompletion(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t, legacyMemoryItem{ID: "one", Scope: "global", Summary: "one", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	req := LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity}
	svc.wal = &failCompletionAppender{Appender: store, fail: true}
	if _, err := svc.MigrateLegacy(context.Background(), req); err == nil {
		t.Fatal("injected completion failure was ignored")
	}
	var existing migrationStage
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Type == legacyMigrationStage {
				_ = json.Unmarshal(event.Data, &existing)
			}
		}
	}
	extra := existing
	extra.Index++
	extra.Digest, _ = migrationStageDigest(extra)
	payload, _ := json.Marshal(extra)
	if _, err := store.Append(wal.Transaction{ID: "valid-extra", IdempotencyKey: "valid-extra", Principal: "alice", Actor: "corrupt",
		Events: []wal.Event{{Store: artifactStore, Type: legacyMigrationStage, Data: payload}}}); err != nil {
		t.Fatal(err)
	}
	svc.wal = store
	if _, err := svc.MigrateLegacy(context.Background(), req); err == nil || !strings.Contains(err.Error(), "freshly recomputed") {
		t.Fatalf("valid extra stage was accepted: %v", err)
	}
	if _, ok, err := svc.Show("one"); err != nil || ok {
		t.Fatalf("inert orphan stage became visible or disrupted ordinary reads: ok=%v err=%v", ok, err)
	}
}

func TestDuplicateLegacyArtifactCreateIsQuarantined(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	item := legacyMemoryItem{ID: "duplicate", MemoryKind: "memory", Scope: "global", Summary: "first", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now}
	appendLegacyArtifactHead(t, store, item, 1, AuthorityCandidate, nil, nil)
	// A second create is invalid history even when it happens to be identical.
	body, _ := json.Marshal(struct {
		Artifact legacyArtifactWire `json:"artifact"`
	}{legacyArtifactWire{ID: item.ID, Version: 1, Kind: "stado.dev/bundled/learn#memory", Scope: ScopeGlobal,
		Authority: AuthorityCandidate, Sensitivity: "normal", Data: json.RawMessage(`{"summary":"first"}`), CreatedAt: now, UpdatedAt: now}})
	if _, err := store.Append(wal.Transaction{ID: "duplicate-create", IdempotencyKey: "duplicate-create", Principal: "alice", Actor: "legacy",
		Events: []wal.Event{{Store: artifactStore, Type: "artifact.create", Data: body}}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.MigrateLegacy(context.Background(), LegacyMigration{ArchiveDigest: archiveDigest(nil), Principal: "alice", Actor: "plugin", Identity: identity})
	if err == nil || len(result.Quarantined) == 0 {
		t.Fatalf("duplicate create accepted: result=%+v err=%v", result, err)
	}
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Type == legacyCompletedEvent {
				t.Fatal("duplicate history wrote a completion marker")
			}
		}
	}
}

func TestFinalSourceValidatorRunsAfterStagesBeforeMarker(t *testing.T) {
	svc, store, identity := migrationFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	raw := legacyLog(t, legacyMemoryItem{ID: "raced", Scope: "global", Summary: "raced", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now})
	validated := false
	_, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: raw, ArchiveDigest: archiveDigest(raw), Principal: "alice", Actor: "plugin", Identity: identity,
		ValidateSource: func() error {
			validated = true
			for _, record := range store.Records() {
				for _, event := range record.Transaction.Events {
					if event.Type == legacyMigrationStage {
						return errors.New("source changed after stage append")
					}
				}
			}
			return errors.New("validator ran before staging")
		},
	})
	if err == nil || !validated {
		t.Fatalf("final validator result: validated=%v err=%v", validated, err)
	}
	if _, ok, showErr := svc.Show("raced"); showErr != nil || ok {
		t.Fatalf("source race exposed staged artifact: ok=%v err=%v", ok, showErr)
	}
	for _, record := range store.Records() {
		for _, event := range record.Transaction.Events {
			if event.Type == legacyCompletedEvent {
				t.Fatal("source race wrote completion marker")
			}
		}
	}
}
