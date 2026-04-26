package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreCRUD(t *testing.T) {
	var tick int
	store := Store{
		Path: filepath.Join(t.TempDir(), "tasks.json"),
		Now: func() time.Time {
			tick++
			return time.Date(2026, 4, 25, 12, tick, 0, 0, time.UTC)
		},
		NewID: func() string { return "task-1" },
	}

	created, err := store.Create(" Ship tasks ", " keep it small ", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Title != "Ship tasks" || created.Body != "keep it small" || created.Status != StatusOpen {
		t.Fatalf("created = %+v", created)
	}

	status := StatusInProgress
	title := "Ship task manager"
	updated, err := store.Update(created.ID, Patch{Title: &title, Status: &status})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != title || updated.Status != StatusInProgress {
		t.Fatalf("updated = %+v", updated)
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Fatalf("UpdatedAt should advance: %+v", updated)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != title {
		t.Fatalf("Get title = %q, want %q", got.Title, title)
	}

	list, err := store.List(StatusInProgress)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("filtered list = %+v", list)
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = store.List("")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("tasks after delete = %+v", list)
	}
}

func TestStoreValidation(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "tasks.json")}
	if _, err := store.Create(" ", "", ""); err == nil {
		t.Fatal("Create with empty title should fail")
	}
	if _, err := store.Create("x", "", Status("blocked")); err == nil {
		t.Fatal("Create with invalid status should fail")
	}
	if _, err := store.Create(strings.Repeat("x", MaxTitleBytes+1), "", ""); err == nil {
		t.Fatal("Create with oversized title should fail")
	}
	if _, err := store.Create("x", strings.Repeat("x", MaxBodyBytes+1), ""); err == nil {
		t.Fatal("Create with oversized body should fail")
	}
	if _, err := store.Get(strings.Repeat("x", MaxIDBytes+1)); err == nil {
		t.Fatal("Get with oversized id should fail")
	}
}

func TestStoreRejectsOversizedLoadedTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	raw, err := json.Marshal([]Task{{
		ID:        "task-1",
		Title:     "loaded",
		Body:      strings.Repeat("x", MaxBodyBytes+1),
		Status:    StatusOpen,
		CreatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Path: path}).Get("task-1"); err == nil {
		t.Fatal("Get should reject persisted tasks that exceed body limit")
	}
}

func TestStoreRejectsSymlinkEscapeOnLoad(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	raw, err := json.Marshal([]Task{{
		ID:        "task-1",
		Title:     "outside",
		Status:    StatusOpen,
		CreatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tasks.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := (Store{Path: path}).Get("task-1"); err == nil {
		t.Fatal("Get should reject a task store symlink that escapes its directory")
	}
}

func TestStoreRejectsParentSymlinkOnLoad(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]Task{{
		ID:        "task-1",
		Title:     "target",
		Status:    StatusOpen,
		CreatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "tasks.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "tasks-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := (Store{Path: filepath.Join(link, "tasks.json")}).Get("task-1"); err == nil {
		t.Fatal("Get should reject symlinked task store parent dirs")
	}
}

func TestStoreDoesNotFollowSymlinkEscapeOnSave(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	const outsideData = "outside data"
	if err := os.WriteFile(outside, []byte(outsideData), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tasks.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	task := Task{
		ID:        "task-2",
		Title:     "inside",
		Status:    StatusOpen,
		CreatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}
	if err := (Store{Path: path}).save([]Task{task}); err != nil {
		t.Fatalf("save: %v", err)
	}
	gotOutside, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutside) != outsideData {
		t.Fatalf("outside file was modified: %q", string(gotOutside))
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("store path mode = %v, want regular file", info.Mode())
	}
	got, err := (Store{Path: path}).Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != task.Title {
		t.Fatalf("Get title = %q, want %q", got.Title, task.Title)
	}
}

func TestStoreRejectsParentSymlinkOnCreate(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "tasks-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	store := Store{Path: filepath.Join(link, "tasks.json")}

	if _, err := store.Create("inside", "", ""); err == nil {
		t.Fatal("Create should reject symlinked task store parent dirs")
	}
	if _, err := os.Stat(filepath.Join(target, "tasks.json")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "tasks.json.lock")); !os.IsNotExist(err) {
		t.Fatalf("symlink target lock was modified, stat err = %v", err)
	}
}

func TestStoreConcurrentCreatesPreserveAllTasks(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "tasks.json")}
	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Create("task "+strconv.Itoa(i), "", StatusOpen)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	list, err := store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != workers {
		t.Fatalf("tasks = %d, want %d: %+v", len(list), workers, list)
	}
}
