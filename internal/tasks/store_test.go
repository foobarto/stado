package tasks

import (
	"path/filepath"
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
}
