package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// resolveEnv builds a clean-room with two sessions + an optional
// third, one of which has a description. Used by every resolve test.
func resolveEnv(t *testing.T, ids []string, describe map[string]string) (*config.Config, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, err := openSidecar(cfg)
	if err != nil {
		restore()
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	for id, desc := range describe {
		_ = runtime.WriteDescription(filepath.Join(cfg.WorktreeDir(), id), desc)
	}
	return cfg, restore
}

// TestResolveSessionID_ExactWins: full id match short-circuits.
func TestResolveSessionID_ExactWins(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"aaaaaaaa-1111-2222-3333-444444444444", "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"},
		nil)
	defer restore()

	got, err := resolveSessionID(cfg, "aaaaaaaa-1111-2222-3333-444444444444")
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "aaaaaaaa-1111-2222-3333-444444444444" {
		t.Errorf("got %q", got)
	}
}

// TestResolveSessionID_PrefixUnique: 8+ char prefix that matches one
// id → resolves to that id.
func TestResolveSessionID_PrefixUnique(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"aaaaaaaa-1111-2222", "bbbbbbbb-aaaa-cccc"},
		nil)
	defer restore()

	got, err := resolveSessionID(cfg, "aaaaaaaa")
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "aaaaaaaa-1111-2222" {
		t.Errorf("got %q", got)
	}
}

// TestResolveSessionID_PrefixAmbiguous: 8+ char prefix matching
// multiple → error listing candidates.
func TestResolveSessionID_PrefixAmbiguous(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"aaaaaaaa-1111", "aaaaaaaa-2222"},
		nil)
	defer restore()

	_, err := resolveSessionID(cfg, "aaaaaaaa")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity: %v", err)
	}
}

// TestResolveSessionID_PrefixTooShort: <8 chars isn't treated as a
// prefix (avoids matching too aggressively — "a" would be a
// trainwreck with UUIDs).
func TestResolveSessionID_PrefixTooShort(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"aaaaaaaa-1111-2222"},
		nil)
	defer restore()

	_, err := resolveSessionID(cfg, "aaa")
	if err == nil {
		t.Fatal("3-char lookup should NOT match a UUID prefix")
	}
}

// TestResolveSessionID_DescriptionUnique: substring match against a
// described session wins.
func TestResolveSessionID_DescriptionUnique(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"s-alpha", "s-beta"},
		map[string]string{"s-alpha": "the react refactor session"})
	defer restore()

	got, err := resolveSessionID(cfg, "react")
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "s-alpha" {
		t.Errorf("got %q, want s-alpha", got)
	}
}

// TestResolveSessionID_DescriptionCaseInsensitive: REACT → react.
func TestResolveSessionID_DescriptionCaseInsensitive(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"s-a"},
		map[string]string{"s-a": "React migration"})
	defer restore()

	got, err := resolveSessionID(cfg, "REACT")
	if err != nil {
		t.Fatalf("case-insensitive lookup failed: %v", err)
	}
	if got != "s-a" {
		t.Errorf("got %q", got)
	}
}

// TestResolveSessionID_DescriptionAmbiguous: two sessions share a
// substring in their descriptions → error listing both.
func TestResolveSessionID_DescriptionAmbiguous(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"s-a", "s-b"},
		map[string]string{
			"s-a": "react hook refactor",
			"s-b": "react component rewrite",
		})
	defer restore()

	_, err := resolveSessionID(cfg, "react")
	if err == nil {
		t.Fatal("expected ambiguity")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity: %v", err)
	}
	if !strings.Contains(err.Error(), "s-a") || !strings.Contains(err.Error(), "s-b") {
		t.Errorf("error should list both candidates: %v", err)
	}
}

// TestResolveSessionID_NoMatch: nothing matches → error explains
// the three strategies tried.
func TestResolveSessionID_NoMatch(t *testing.T) {
	cfg, restore := resolveEnv(t,
		[]string{"s-a"},
		nil)
	defer restore()

	_, err := resolveSessionID(cfg, "definitely-not-a-session")
	if err == nil {
		t.Fatal("expected no-match error")
	}
	if !strings.Contains(err.Error(), "no session matches") {
		t.Errorf("error should mention no-match: %v", err)
	}
}

// TestResolveSessionID_EmptyLookupErrors.
func TestResolveSessionID_EmptyLookupErrors(t *testing.T) {
	cfg, restore := resolveEnv(t, []string{"s-a"}, nil)
	defer restore()

	_, err := resolveSessionID(cfg, "")
	if err == nil {
		t.Fatal("empty lookup should error")
	}
}
