package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/memory"
)

func TestLegacyMigrationPreservesAuthorityScopeAndArchive(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	items := []memory.Item{
		{ID: "mem_old", MemoryKind: "memory", Scope: "repo", RepoID: "repo-1", Summary: "ordinary", Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now},
		{ID: "lesson_old", MemoryKind: "lesson", Scope: "session", SessionID: "session-1", Summary: "lesson", Lesson: "do this", Trigger: "when x", Confidence: "approved", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now},
		{ID: "bad_scope", MemoryKind: "memory", Scope: "session", SessionID: "unknown", Summary: "bad", Confidence: "candidate", Sensitivity: "normal", CreatedAt: now, UpdatedAt: now},
	}
	raw := []byte("original legacy bytes\n")
	archive := filepath.Join(t.TempDir(), "legacy.jsonl")
	res, err := svc.MigrateLegacy(context.Background(), LegacyMigration{Items: items, RawLog: raw, ArchivePath: archive, Principal: "alice", Actor: "migration", ValidateSessionAnchor: func(id string) bool { return id == "session-1" }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Migrated != 2 || len(res.Quarantined) != 1 || res.Quarantined[0] != "bad_scope" {
		t.Fatalf("result=%+v", res)
	}
	mem, ok, err := svc.Show("mem_old")
	if err != nil || !ok || mem.Authority != AuthorityActive {
		t.Fatalf("memory=%+v ok=%v err=%v", mem, ok, err)
	}
	lesson, ok, err := svc.Show("lesson_old")
	if err != nil || !ok || lesson.Authority != AuthorityLegacyActive || lesson.Binding.AnchorSessionID != "session-1" {
		t.Fatalf("lesson=%+v ok=%v err=%v", lesson, ok, err)
	}
	got, err := os.ReadFile(archive)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("archive=%q err=%v", got, err)
	}
	// Same converter input is a WAL-idempotent retry.
	if _, err := svc.MigrateLegacy(context.Background(), LegacyMigration{Items: items, RawLog: raw, ArchivePath: archive, Principal: "alice", Actor: "migration", ValidateSessionAnchor: func(id string) bool { return id == "session-1" }}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyArchiveMismatchFailsClosed(t *testing.T) {
	svc, _, store := fixture(t)
	defer store.Close()
	archive := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(archive, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MigrateLegacy(context.Background(), LegacyMigration{RawLog: []byte("expected"), ArchivePath: archive, Principal: "alice", Actor: "migration"}); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}
