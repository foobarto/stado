package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
)

func TestFixedLegacySourceChangeAfterStageBeforeCompletionFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	event, err := json.Marshal(map[string]any{
		"type": "memory", "action": "upsert", "id": "race", "actor": "legacy", "timestamp": now,
		"item": map[string]any{
			"id": "race", "scope": "global", "kind": "note", "summary": "before",
			"confidence": "candidate", "sensitivity": "normal", "created_at": now, "updated_at": now,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := append(event, '\n')
	sourcePath := filepath.Join(stateDir, filepath.FromSlash(legacyMemorySourcePath))
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	source := &legacyMemorySource{root: root}
	snapshot, archiveDigest, err := source.readArchiveAndRecheckLocked()
	if err != nil {
		t.Fatal(err)
	}

	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	definitions := []plugins.ArtifactKindDef{
		{Name: "memory", Schema: `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"},"trigger":{"type":"string"}}}`},
		{Name: "lesson", Schema: `{"type":"object","additionalProperties":false,"required":["summary","trigger"],"properties":{"summary":{"type":"string","minLength":1},"content":{"type":"string"},"trigger":{"type":"string","minLength":1},"expected_outcome":{"type":"string"}}}`},
	}
	manifest := plugins.Manifest{Name: "memory", Version: "v1.0.0", ArtifactKinds: definitions}
	runtimeIdentity, err := plugins.RuntimeIdentityForLocalSource(manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := artifacts.NewKindRegistry()
	if err := registry.Register(runtimeIdentity, definitions); err != nil {
		t.Fatal(err)
	}
	memoryKind, _ := runtimeIdentity.QualifiedKind("memory")
	lessonKind, _ := runtimeIdentity.QualifiedKind("lesson")
	memoryDescriptor, _ := registry.Lookup(artifacts.Kind(memoryKind))
	lessonDescriptor, _ := registry.Lookup(artifacts.Kind(lessonKind))
	service := artifacts.NewServiceWithKinds(store, nil, registry)
	identity := artifacts.LegacyMigrationIdentity{Runtime: runtimeIdentity, Memory: memoryDescriptor, Lesson: lessonDescriptor}

	mutated := false
	result, err := service.MigrateLegacy(context.Background(), artifacts.LegacyMigration{
		RawLog: snapshot, ArchiveDigest: archiveDigest, Principal: "alice", Actor: "plugin:" + runtimeIdentity.Canonical, Identity: identity,
		ValidateSource: func() error {
			staged := false
			for _, record := range store.Records() {
				for _, walEvent := range record.Transaction.Events {
					staged = staged || walEvent.Type == "artifact.migration.stage"
				}
			}
			if !staged {
				return errors.New("final source validator ran before any migration stage")
			}
			mutated = true
			if writeErr := os.WriteFile(sourcePath, append(append([]byte(nil), raw...), []byte(" ")...), 0o600); writeErr != nil {
				return writeErr
			}
			return source.validateFixedSnapshot(snapshot, archiveDigest)
		},
	})
	if err == nil || !mutated || result.Complete {
		t.Fatalf("source race committed: result=%+v mutated=%v err=%v", result, mutated, err)
	}
	if !strings.Contains(err.Error(), "source changed before completion") {
		t.Fatalf("unexpected source-race error: %v", err)
	}
	for _, record := range store.Records() {
		for _, walEvent := range record.Transaction.Events {
			if walEvent.Type == "migration.completed.legacy-memory-v1" {
				t.Fatal("source race appended a completion marker")
			}
		}
	}
	if _, ok, showErr := service.Show("race"); showErr != nil || ok {
		t.Fatalf("staged item became canonically visible: ok=%v err=%v", ok, showErr)
	}
	archived, err := os.ReadFile(filepath.Join(stateDir, filepath.FromSlash(legacyMemoryArchive)))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(archived); "sha256:"+hex.EncodeToString(sum[:]) != archiveDigest || string(archived) != string(snapshot) {
		t.Fatal("fsynced archive changed after the source race")
	}
}
