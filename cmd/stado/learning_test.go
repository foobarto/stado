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
