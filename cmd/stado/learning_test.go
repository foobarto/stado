package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/memory"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestLearningCLI_ProposeListShowAndApprove(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()

	proposeCmd := newLearningProposeCmd()
	for name, value := range map[string]string{
		"summary":  "Use pinned Go toolchain",
		"lesson":   "Use the pinned toolchain path before declaring Go unavailable.",
		"trigger":  "When Go tooling is missing from PATH.",
		"evidence": "Local verification used the repo-pinned Go binary.",
		"tags":     "tooling, go",
	} {
		if err := proposeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := proposeCmd.Flags().Set("test", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	if err := proposeCmd.RunE(proposeCmd, nil); err != nil {
		t.Fatalf("learning propose: %v", err)
	}

	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	lessons := filterLessons(items)
	if len(lessons) != 1 {
		t.Fatalf("expected one lesson, got %+v", lessons)
	}
	lesson := lessons[0]
	if lesson.Confidence != "candidate" || !strings.HasPrefix(lesson.ID, "lesson_") {
		t.Fatalf("unexpected lesson candidate: %+v", lesson)
	}
	if lesson.Trigger != "When Go tooling is missing from PATH." || lesson.Evidence.Notes == "" {
		t.Fatalf("lesson metadata missing: %+v", lesson)
	}
	if strings.Join(lesson.Tags, ",") != "tooling,go" {
		t.Fatalf("tags = %#v", lesson.Tags)
	}

	listCmd := newLearningListCmd()
	out := captureStdout(t, func() {
		if err := listCmd.RunE(listCmd, nil); err != nil {
			t.Fatalf("learning list: %v", err)
		}
	})
	if !strings.Contains(out, shortMemory(lesson.ID, 20)) || !strings.Contains(out, "candidate") {
		t.Fatalf("learning list missing candidate:\n%s", out)
	}

	showCmd := newLearningShowCmd()
	out = captureStdout(t, func() {
		if err := showCmd.RunE(showCmd, []string{lesson.ID}); err != nil {
			t.Fatalf("learning show: %v", err)
		}
	})
	var shown memory.Item
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("show JSON: %v\n%s", err, out)
	}
	if shown.ID != lesson.ID || !memory.IsLesson(shown) {
		t.Fatalf("unexpected shown lesson: %+v", shown)
	}

	if err := memoryApproveCmd.RunE(memoryApproveCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("approve lesson: %v", err)
	}
	result, err := store.Query(ctx, memory.Query{RepoID: lesson.RepoID, Prompt: "go tooling unavailable", MemoryKind: "lesson"})
	if err != nil {
		t.Fatalf("lesson query: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Item.ID != lesson.ID {
		t.Fatalf("approved lesson not queryable: %+v", result)
	}
}

func TestLearningCLI_ProposeRequiresEvidence(t *testing.T) {
	_ = setupMemoryEnv(t)
	proposeCmd := newLearningProposeCmd()
	for name, value := range map[string]string{
		"summary": "Avoid stale claims",
		"lesson":  "Run the relevant verification gate before reporting success.",
		"trigger": "When a task changes shared behavior.",
	} {
		if err := proposeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	err := proposeCmd.RunE(proposeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("expected evidence validation error, got %v", err)
	}
}

func TestLearningCLI_ExportJSONFiltersLessons(t *testing.T) {
	store := setupMemoryEnv(t)
	lesson := proposeLearningTestLesson(t, store, "Export only lesson items")
	ordinary := memory.Item{
		ID:         "mem_export_filter",
		Scope:      "global",
		Kind:       "fact",
		Summary:    "Ordinary memory should not appear",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &ordinary})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert ordinary memory: %v", err)
	}

	exportCmd := newLearningExportCmd()
	out := captureStdout(t, func() {
		if err := exportCmd.RunE(exportCmd, nil); err != nil {
			t.Fatalf("learning export: %v", err)
		}
	})
	var exported memory.Export
	if err := json.Unmarshal([]byte(out), &exported); err != nil {
		t.Fatalf("export JSON: %v\n%s", err, out)
	}
	if len(exported.Items) != 1 || exported.Items[0].ID != lesson.ID {
		t.Fatalf("unexpected learning export: %+v", exported)
	}
	if !memory.IsLesson(exported.Items[0]) {
		t.Fatalf("exported non-lesson item: %+v", exported.Items[0])
	}
}

func TestLearningCLI_ProposeUsesSessionWorktreeRepoPin(t *testing.T) {
	store := setupMemoryEnv(t)
	root := t.TempDir()
	realRepo := filepath.Join(root, "real-repo")
	sessionWorktree := filepath.Join(root, "session-worktree")
	sessionSubdir := filepath.Join(sessionWorktree, "nested")
	if err := os.MkdirAll(filepath.Join(realRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionWorktree, ".stado"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionWorktree, ".stado", "user-repo"), []byte(realRepo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionSubdir, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := chdir(t, sessionSubdir)
	t.Cleanup(restore)

	proposeCmd := newLearningProposeCmd()
	for name, value := range map[string]string{
		"summary":  "Use session repo pins",
		"lesson":   "Use the pinned user repo path when proposing repo-scoped lessons from a session worktree.",
		"trigger":  "When the cwd is inside a session worktree.",
		"evidence": "The session worktree records .stado/user-repo.",
	} {
		if err := proposeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := proposeCmd.RunE(proposeCmd, nil); err != nil {
		t.Fatalf("learning propose: %v", err)
	}

	expectedRepoID, err := stadogit.RepoID(realRepo)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	lessons := filterLessons(items)
	if len(lessons) != 1 {
		t.Fatalf("expected one lesson, got %+v", lessons)
	}
	if lessons[0].RepoID != expectedRepoID {
		t.Fatalf("lesson repo_id = %q, want pinned repo %q", lessons[0].RepoID, expectedRepoID)
	}
}

func TestLearningCLI_ReadCurrentRepoPinRejectsStadoDirSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "user-repo"), []byte("/outside/repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(workdir, ".stado")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if got := readCurrentRepoPin(workdir); got != "" {
		t.Fatalf("readCurrentRepoPin followed .stado symlink escape: %q", got)
	}
}

func TestLearningCLI_MemoryEditUpdatesLessonBody(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()
	proposeCmd := newLearningProposeCmd()
	for name, value := range map[string]string{
		"summary":  "Keep lesson edits coherent",
		"lesson":   "Old lesson wording.",
		"trigger":  "When a lesson needs review.",
		"evidence": "A reviewer edited the candidate.",
	} {
		if err := proposeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := proposeCmd.RunE(proposeCmd, nil); err != nil {
		t.Fatalf("learning propose: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	lessons := filterLessons(items)
	if len(lessons) != 1 {
		t.Fatalf("expected one lesson, got %+v", lessons)
	}

	editCmd := newMemoryEditCmd()
	if err := editCmd.Flags().Set("body", "Edited lesson wording."); err != nil {
		t.Fatal(err)
	}
	if err := editCmd.RunE(editCmd, []string{lessons[0].ID}); err != nil {
		t.Fatalf("memory edit lesson: %v", err)
	}
	edited, ok, err := store.Show(ctx, lessons[0].ID)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !ok {
		t.Fatal("edited lesson missing")
	}
	if edited.Body != "Edited lesson wording." || edited.Lesson != "Edited lesson wording." {
		t.Fatalf("lesson body fields not synced after edit: %+v", edited)
	}
}

func TestLearningCLI_EditLessonFieldsAndApprove(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()
	lesson := proposeLearningTestLesson(t, store, "Keep lesson review explicit")

	editCmd := newLearningEditCmd()
	for name, value := range map[string]string{
		"summary":   "Keep lesson review explicit before approval",
		"lesson":    "Use `stado learning edit` to fix trigger, rationale, and evidence before approving.",
		"trigger":   "When a lesson candidate is vague or missing provenance.",
		"rationale": "Lesson prompt context is durable, so review metadata must be coherent.",
		"evidence":  "The candidate was edited through the learning review command.",
		"tags":      "learning, review",
	} {
		if err := editCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := editCmd.Flags().Set("test", "go test ./cmd/stado"); err != nil {
		t.Fatal(err)
	}
	if err := editCmd.RunE(editCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("learning edit: %v", err)
	}

	edited, ok, err := store.Show(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("Show edited: %v", err)
	}
	if !ok {
		t.Fatal("edited lesson missing")
	}
	if edited.Lesson != "Use `stado learning edit` to fix trigger, rationale, and evidence before approving." || edited.Body != edited.Lesson {
		t.Fatalf("lesson/body not updated together: %+v", edited)
	}
	if edited.Trigger != "When a lesson candidate is vague or missing provenance." || edited.Rationale == "" {
		t.Fatalf("lesson review metadata missing: %+v", edited)
	}
	if edited.Evidence.Notes == "" || strings.Join(edited.Evidence.Tests, ",") != "go test ./cmd/stado" {
		t.Fatalf("lesson evidence not replaced: %+v", edited.Evidence)
	}
	if strings.Join(edited.Tags, ",") != "learning,review" {
		t.Fatalf("tags = %#v", edited.Tags)
	}

	approveCmd := newLearningActionCmd("approve", "Approve a lesson candidate")
	if err := approveCmd.RunE(approveCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("learning approve: %v", err)
	}
	result, err := store.Query(ctx, memory.Query{
		RepoID:     edited.RepoID,
		Prompt:     "vague lesson provenance",
		MemoryKind: "lesson",
	})
	if err != nil {
		t.Fatalf("lesson query: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Item.ID != lesson.ID {
		t.Fatalf("approved edited lesson not queryable: %+v", result)
	}
}

func TestLearningCLI_ActionsOnlyAffectLessons(t *testing.T) {
	store := setupMemoryEnv(t)
	item := memory.Item{
		ID:         "mem_not_lesson",
		Scope:      "global",
		Kind:       "fact",
		Summary:    "Ordinary memory",
		Confidence: "candidate",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert ordinary memory: %v", err)
	}

	approveCmd := newLearningActionCmd("approve", "Approve a lesson candidate")
	err := approveCmd.RunE(approveCmd, []string{item.ID})
	if err == nil || !strings.Contains(err.Error(), "lesson") {
		t.Fatalf("expected lesson-only validation error, got %v", err)
	}
}

func TestLearningCLI_SupersedeApprovedLesson(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()
	lesson := proposeLearningTestLesson(t, store, "Refresh stale release lessons")

	approveCmd := newLearningActionCmd("approve", "Approve a lesson candidate")
	if err := approveCmd.RunE(approveCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("learning approve: %v", err)
	}

	supersedeCmd := newLearningSupersedeCmd()
	for name, value := range map[string]string{
		"summary":  "Verify releases with the current release checklist",
		"lesson":   "Use the current release checklist before declaring a release complete.",
		"trigger":  "When cutting or validating a release.",
		"evidence": "The prior release lesson was stale.",
	} {
		if err := supersedeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := supersedeCmd.RunE(supersedeCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("learning supersede: %v", err)
	}

	old, ok, err := store.Show(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("Show old: %v", err)
	}
	if !ok {
		t.Fatal("superseded lesson missing")
	}
	if old.Confidence != "superseded" {
		t.Fatalf("old confidence = %q, want superseded", old.Confidence)
	}

	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var replacement memory.Item
	for _, item := range items {
		if item.ID != lesson.ID && memorySupersedes(item, lesson.ID) {
			replacement = item
			break
		}
	}
	if replacement.ID == "" {
		t.Fatalf("replacement lesson missing from folded list: %+v", items)
	}
	if !memory.IsLesson(replacement) || replacement.Confidence != "approved" {
		t.Fatalf("unexpected replacement lesson: %+v", replacement)
	}
	if replacement.Trigger != "When cutting or validating a release." || replacement.Evidence.Notes == "" {
		t.Fatalf("replacement metadata missing: %+v", replacement)
	}
	result, err := store.Query(ctx, memory.Query{
		RepoID:     replacement.RepoID,
		Prompt:     "validate release checklist",
		MemoryKind: "lesson",
	})
	if err != nil {
		t.Fatalf("lesson query: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Item.ID != replacement.ID {
		t.Fatalf("replacement lesson not queryable: %+v", result)
	}
}

func TestLearningCLI_DocumentWritesLearningNoteAndRejects(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()
	lesson := proposeLearningTestLesson(t, store, "Document durable repo guidance")

	documentCmd := newLearningDocumentCmd()
	if err := documentCmd.RunE(documentCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("learning document: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(".learnings", "document-durable-repo-guidance-"+lesson.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("documented lesson file missing, got %v", files)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Document durable repo guidance",
		"## Trigger",
		"When reviewing operational lesson candidates.",
		"## Lesson",
		"Use the explicit learning review flow before approval.",
		"## Evidence",
		"Unit test proposal.",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("documented lesson missing %q:\n%s", want, string(body))
		}
	}

	documented, ok, err := store.Show(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("Show documented: %v", err)
	}
	if !ok {
		t.Fatal("documented lesson missing from store")
	}
	if documented.Confidence != "rejected" {
		t.Fatalf("documented lesson confidence = %q, want rejected", documented.Confidence)
	}
	result, err := store.Query(ctx, memory.Query{
		RepoID:     lesson.RepoID,
		Prompt:     "durable repo guidance",
		MemoryKind: "lesson",
	})
	if err != nil {
		t.Fatalf("lesson query: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("documented rejected lesson should not be queryable: %+v", result)
	}
}

func TestLearningCLI_DocumentRejectsLearningsDirSymlinkEscape(t *testing.T) {
	store := setupMemoryEnv(t)
	lesson := proposeLearningTestLesson(t, store, "Reject symlinked learning docs")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cwd, ".learnings")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	documentCmd := newLearningDocumentCmd()
	err = documentCmd.RunE(documentCmd, []string{lesson.ID})
	if err == nil || !strings.Contains(err.Error(), ".learnings") {
		t.Fatalf("expected .learnings symlink rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, lessonDocumentFilename(lesson))); !os.IsNotExist(err) {
		t.Fatalf("document write escaped through .learnings symlink: %v", err)
	}
	after, ok, err := store.Show(context.Background(), lesson.ID)
	if err != nil {
		t.Fatalf("Show after failed document: %v", err)
	}
	if !ok || after.Confidence != "candidate" {
		t.Fatalf("failed document should leave lesson candidate, got %+v", after)
	}
}

func TestLearningCLI_DocumentRejectsNestedSymlinkEscape(t *testing.T) {
	store := setupMemoryEnv(t)
	lesson := proposeLearningTestLesson(t, store, "Reject nested learning symlinks")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".learnings"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cwd, ".learnings", "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	documentCmd := newLearningDocumentCmd()
	if err := documentCmd.Flags().Set("path", "escape/review-note.md"); err != nil {
		t.Fatal(err)
	}
	err = documentCmd.RunE(documentCmd, []string{lesson.ID})
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("expected nested symlink rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "review-note.md")); !os.IsNotExist(err) {
		t.Fatalf("document write escaped through nested symlink: %v", err)
	}
	after, ok, err := store.Show(context.Background(), lesson.ID)
	if err != nil {
		t.Fatalf("Show after failed document: %v", err)
	}
	if !ok || after.Confidence != "candidate" {
		t.Fatalf("failed document should leave lesson candidate, got %+v", after)
	}
}

func TestLearningCLI_DocumentRefusesOverwrite(t *testing.T) {
	store := setupMemoryEnv(t)
	first := proposeLearningTestLesson(t, store, "Document first lesson")
	second := proposeLearningTestLesson(t, store, "Document second lesson")

	documentCmd := newLearningDocumentCmd()
	if err := documentCmd.Flags().Set("path", "review-note.md"); err != nil {
		t.Fatal(err)
	}
	if err := documentCmd.RunE(documentCmd, []string{first.ID}); err != nil {
		t.Fatalf("learning document first: %v", err)
	}

	documentAgain := newLearningDocumentCmd()
	if err := documentAgain.Flags().Set("path", "review-note.md"); err != nil {
		t.Fatal(err)
	}
	err := documentAgain.RunE(documentAgain, []string{second.ID})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected overwrite refusal, got %v", err)
	}
	secondAfter, ok, err := store.Show(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("Show second: %v", err)
	}
	if !ok {
		t.Fatal("second lesson missing")
	}
	if secondAfter.Confidence != "candidate" {
		t.Fatalf("failed document should leave second lesson candidate, got %q", secondAfter.Confidence)
	}
}

func TestLearningCLI_StaleMarksMissingEvidenceFilesCandidate(t *testing.T) {
	store := setupMemoryEnv(t)
	ctx := context.Background()
	proposeCmd := newLearningProposeCmd()
	for name, value := range map[string]string{
		"summary":  "Recheck missing file evidence",
		"lesson":   "Review lessons again when their evidence files disappear.",
		"trigger":  "When a lesson cites a deleted source file.",
		"evidence": "The source file was deleted after approval.",
	} {
		if err := proposeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := proposeCmd.Flags().Set("file", "missing.go"); err != nil {
		t.Fatal(err)
	}
	if err := proposeCmd.RunE(proposeCmd, nil); err != nil {
		t.Fatalf("learning propose: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	lessons := filterLessons(items)
	if len(lessons) != 1 {
		t.Fatalf("expected one lesson, got %+v", lessons)
	}
	lesson := lessons[0]
	approveCmd := newLearningActionCmd("approve", "Approve a lesson candidate")
	if err := approveCmd.RunE(approveCmd, []string{lesson.ID}); err != nil {
		t.Fatalf("learning approve: %v", err)
	}
	result, err := store.Query(ctx, memory.Query{
		RepoID:     lesson.RepoID,
		Prompt:     "missing file evidence",
		MemoryKind: "lesson",
	})
	if err != nil {
		t.Fatalf("lesson query before stale apply: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("approved lesson should be queryable before stale apply: %+v", result)
	}

	staleCmd := newLearningStaleCmd()
	out := captureStdout(t, func() {
		if err := staleCmd.RunE(staleCmd, nil); err != nil {
			t.Fatalf("learning stale dry-run: %v", err)
		}
	})
	if !strings.Contains(out, shortMemory(lesson.ID, 20)) || !strings.Contains(out, "missing.go") {
		t.Fatalf("stale dry-run missing lesson/file:\n%s", out)
	}
	stillApproved, ok, err := store.Show(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("Show after dry-run: %v", err)
	}
	if !ok || stillApproved.Confidence != "approved" {
		t.Fatalf("dry-run changed lesson: %+v", stillApproved)
	}

	applyCmd := newLearningStaleCmd()
	if err := applyCmd.Flags().Set("apply", "true"); err != nil {
		t.Fatal(err)
	}
	if err := applyCmd.RunE(applyCmd, nil); err != nil {
		t.Fatalf("learning stale --apply: %v", err)
	}
	staleLesson, ok, err := store.Show(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("Show after apply: %v", err)
	}
	if !ok || staleLesson.Confidence != "candidate" {
		t.Fatalf("stale apply should mark candidate, got %+v", staleLesson)
	}
	result, err = store.Query(ctx, memory.Query{
		RepoID:     lesson.RepoID,
		Prompt:     "missing file evidence",
		MemoryKind: "lesson",
	})
	if err != nil {
		t.Fatalf("lesson query after stale apply: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("stale candidate lesson should not be queryable: %+v", result)
	}
}

func TestLearningCLI_ListStripsControlChars(t *testing.T) {
	store := setupMemoryEnv(t)
	item := memory.Item{
		ID:         "lesson_control_summary",
		MemoryKind: "lesson",
		Scope:      "global",
		Summary:    "Safe\x1b[2J summary",
		Lesson:     "Keep review tables safe.",
		Trigger:    "When listing plugin-proposed lessons.",
		Evidence:   memory.Evidence{Notes: "unit test"},
		Confidence: "candidate",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatalf("upsert lesson: %v", err)
	}

	listCmd := newLearningListCmd()
	out := captureStdout(t, func() {
		if err := listCmd.RunE(listCmd, nil); err != nil {
			t.Fatalf("learning list: %v", err)
		}
	})
	if strings.Contains(out, "\x1b") {
		t.Fatalf("learning list leaked control character:\n%q", out)
	}
	if !strings.Contains(out, "Safe[2J summary") {
		t.Fatalf("learning list missing sanitized summary:\n%s", out)
	}
}

func proposeLearningTestLesson(t *testing.T, store *memory.Store, summary string) memory.Item {
	t.Helper()
	proposeCmd := newLearningProposeCmd()
	for name, value := range map[string]string{
		"summary":  summary,
		"lesson":   "Use the explicit learning review flow before approval.",
		"trigger":  "When reviewing operational lesson candidates.",
		"evidence": "Unit test proposal.",
	} {
		if err := proposeCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := proposeCmd.RunE(proposeCmd, nil); err != nil {
		t.Fatalf("learning propose: %v", err)
	}
	items, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range items {
		if memory.IsLesson(item) && item.Summary == summary {
			return item
		}
	}
	t.Fatalf("lesson %q not found in %+v", summary, items)
	return memory.Item{}
}
