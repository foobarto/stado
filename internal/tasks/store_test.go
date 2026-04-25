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
