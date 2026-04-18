package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	// Verify tables exist by running a query
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if count < 5 {
		t.Errorf("Expected at least 5 tables, got %d", count)
	}
}

func TestCreateSession(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	err = db.CreateSession(ctx, "test-1", "anthropic", "claude-sonnet-4-20250514", "/tmp")
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	var provider, model string
	err = db.QueryRow("SELECT provider, model FROM sessions WHERE id = ?", "test-1").Scan(&provider, &model)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if provider != "anthropic" {
		t.Errorf("provider = %q, want %q", provider, "anthropic")
	}
	if model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", model, "claude-sonnet-4-20250514")
	}
}

func TestAppendMessage(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	err = db.CreateSession(ctx, "sess-1", "anthropic", "claude-sonnet-4-20250514", "/tmp")
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	err = db.AppendMessage(ctx, "sess-1", "msg-1", "user", `"hello"`, 0)
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}

	var role, content string
	err = db.QueryRow("SELECT role, content_json FROM messages WHERE id = ?", "msg-1").Scan(&role, &content)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if role != "user" {
		t.Errorf("role = %q, want %q", role, "user")
	}
	if content != `"hello"` {
		t.Errorf("content_json = %q, want %q", content, `"hello"`)
	}
}

func TestStateDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("State dir was not created")
	}
}
