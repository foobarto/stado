package workdirpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Phase 2.1.b tests for UserConfigResolver. The trust model
// invariants are:
//
//   1. The XDG_CONFIG_HOME / XDG_DATA_HOME / XDG_STATE_HOME /
//      XDG_CACHE_HOME / HOME longest-covering anchor is selected.
//   2. The chain ABOVE the anchor accepts system symlinks (so
//      `/home → /var/home` on Fedora Atomic / Bazzite works).
//   3. The chain BELOW the anchor is no-symlink — an in-user-
//      space attacker can't redirect through a planted symlink.
//   4. When the path has no covering anchor, the resolver falls
//      back to strict no-symlink semantics.
//
// Round-A2 review explicitly called out these axes:
//   - anchor-equality (path == anchor)
//   - overlapping HOME/XDG longest match
//   - symlinked HOME
//   - outside-anchor fallback

// withIsolatedEnv runs body with a clean copy of the relevant
// env vars, then restores them. Keeps tests from leaking state
// into each other or into the real user's environment.
func withIsolatedEnv(t *testing.T, fn func()) {
	t.Helper()
	keys := []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"XDG_STATE_HOME", "XDG_CACHE_HOME", "HOME"}
	saved := make(map[string]string, len(keys))
	hadKey := make(map[string]bool, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		saved[k] = v
		hadKey[k] = ok
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			if hadKey[k] {
				os.Setenv(k, saved[k])
			} else {
				os.Unsetenv(k)
			}
		}
	})
	fn()
}

// ---- Anchor selection ---------------------------------------------------

func TestUserConfigResolver_LongestAnchorWins(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		xdg := filepath.Join(base, "home", "xdg-state")
		if err := os.MkdirAll(xdg, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)
		os.Setenv("XDG_STATE_HOME", xdg)

		// Path under XDG_STATE_HOME — the longer anchor.
		// Both anchors cover it; longest wins.
		target := filepath.Join(xdg, "stado", "session.json")

		uc := NewUserConfigResolver()
		// MkdirAll exercises the anchor walk: the chain UP TO
		// xdg accepts whatever filesystem layout exists, the
		// chain BELOW xdg is created with no-symlink check.
		if err := uc.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("MkdirAll under XDG: %v", err)
		}
		if _, err := os.Stat(filepath.Dir(target)); err != nil {
			t.Errorf("dir not created under XDG anchor: %v", err)
		}
	})
}

func TestUserConfigResolver_AnchorEquality(t *testing.T) {
	// When the requested path EQUALS an anchor exactly, the
	// resolver short-circuits — no walk below.
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		uc := NewUserConfigResolver()
		// Opening the anchor itself should succeed.
		root, err := uc.OpenRoot(home)
		if err != nil {
			t.Fatalf("OpenRoot on anchor: %v", err)
		}
		_ = root.Close()

		// MkdirAll on the anchor itself is a no-op (already exists).
		if err := uc.MkdirAll(home, 0o755); err != nil {
			t.Errorf("MkdirAll on anchor: %v", err)
		}
	})
}

// ---- Symlink-above-anchor (Fedora Atomic case) -------------------------

func TestUserConfigResolver_FollowsAnchorSymlinkAbove(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		realHome := filepath.Join(base, "var", "home", "user")
		if err := os.MkdirAll(realHome, 0o755); err != nil {
			t.Fatal(err)
		}
		// Symlink `/home → /var/home` style.
		linkParent := filepath.Join(base, "home")
		if err := os.Symlink(filepath.Join(base, "var", "home"), linkParent); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		// The HOME the env reports is the symlinked form.
		linkedHome := filepath.Join(linkParent, "user")
		os.Setenv("HOME", linkedHome)

		// Stage a file under the realhome path.
		stadoDir := filepath.Join(realHome, ".local", "share", "stado")
		if err := os.MkdirAll(stadoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(stadoDir, "data.json")
		if err := os.WriteFile(target, []byte(`{"k":"v"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Read via the symlinked HOME path. The anchor walk
		// ABOVE accepts the symlink; the chain BELOW is strict.
		uc := NewUserConfigResolver()
		readPath := filepath.Join(linkedHome, ".local", "share", "stado", "data.json")
		got, err := uc.ReadFileLimited(readPath, 1024)
		if err != nil {
			t.Fatalf("ReadFileLimited via symlinked HOME: %v", err)
		}
		if string(got) != `{"k":"v"}` {
			t.Errorf("contents = %q", got)
		}
	})
}

// ---- Symlink-below-anchor rejected -------------------------------------

func TestUserConfigResolver_RejectsInUserSymlinkBelow(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home", "user")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		// Below the anchor, plant a symlink redirecting the
		// stado dir to /tmp.
		stadoDir := filepath.Join(home, ".local", "share", "stado")
		if err := os.MkdirAll(filepath.Dir(stadoDir), 0o755); err != nil {
			t.Fatal(err)
		}
		// Plant a symlink as the .local directory:
		evil := filepath.Join(home, "victim")
		if err := os.MkdirAll(evil, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(evil, filepath.Join(home, ".local", "share", "redirect")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		uc := NewUserConfigResolver()
		// Reading through the planted symlink should fail.
		probe := filepath.Join(home, ".local", "share", "redirect", "data.json")
		if _, err := uc.ReadFileLimited(probe, 1024); err == nil {
			t.Fatal("expected symlink-below-anchor rejection, got nil")
		}
	})
}

// ---- Outside-anchor fallback to strict ---------------------------------

func TestUserConfigResolver_OutsideAnchorFallsBackToStrict(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		// Path is genuinely outside HOME — falls back to strict
		// no-symlink. A non-existent abs path should error
		// (strict semantics).
		uc := NewUserConfigResolver()
		_, err := uc.OpenRoot(filepath.Join(base, "outside-home", "nonexistent"))
		if err == nil {
			t.Fatal("expected error for nonexistent outside-anchor path, got nil")
		}
	})
}

// ---- ReadFile round-trip + size-limit ---------------------------------

func TestUserConfigResolver_ReadFileLimited_RejectsOversize(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		os.Setenv("HOME", home)
		stadoDir := filepath.Join(home, ".config", "stado")
		if err := os.MkdirAll(stadoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(stadoDir, "config.toml")
		if err := os.WriteFile(target, []byte("more than the limit allows"), 0o644); err != nil {
			t.Fatal(err)
		}

		uc := NewUserConfigResolver()
		if _, err := uc.ReadFileLimited(target, 4); err == nil {
			t.Fatal("expected oversize error, got nil")
		}
	})
}

func TestUserConfigResolver_ReadFileNoLimit_Reads(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		os.Setenv("HOME", home)
		stadoDir := filepath.Join(home, ".config", "stado")
		if err := os.MkdirAll(stadoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(stadoDir, "config.toml")
		if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
			t.Fatal(err)
		}

		uc := NewUserConfigResolver()
		got, err := uc.ReadFileNoLimit(target)
		if err != nil {
			t.Fatalf("ReadFileNoLimit: %v", err)
		}
		if string(got) != "hello world" {
			t.Errorf("contents = %q", got)
		}
	})
}

// ---- MkdirAll creates anchor when missing -----------------------------

func TestUserConfigResolver_MkdirAll_CreatesMissingAnchor(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		// HOME points to a path that doesn't exist yet — the
		// resolver creates the anchor as part of MkdirAll
		// (legacy: "the chain UP TO the anchor is the
		// operator's environment, not adversarial").
		home := filepath.Join(base, "fresh-home")
		os.Setenv("HOME", home)

		uc := NewUserConfigResolver()
		target := filepath.Join(home, ".local", "share", "stado")
		if err := uc.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("target dir not created: %v", err)
		}
	})
}

// ---- NUL-byte rejection ------------------------------------------------

func TestUserConfigResolver_RejectsNULByte(t *testing.T) {
	uc := NewUserConfigResolver()
	bad := "/some/path\x00trailing"
	if _, err := uc.OpenRoot(bad); err == nil {
		t.Error("OpenRoot with NUL byte: expected error, got nil")
	}
	if err := uc.MkdirAll(bad, 0o755); err == nil {
		t.Error("MkdirAll with NUL byte: expected error, got nil")
	}
	if _, err := uc.OpenRegularFile(bad); err == nil {
		t.Error("OpenRegularFile with NUL byte: expected error, got nil")
	}
}

// ---- RemoveAll: closes the EP-0028 RemoveAll gap ---------------------
//
// EP-0028 added the *UnderUserConfig family for read/open/mkdir
// because Atomic Fedora's `/home → /var/home` system symlink
// breaks the strict-from-/ walk. RemoveAll was never given an
// Under-equivalent. UserConfigResolver.RemoveAll fills the gap.

// TestUserConfigResolver_RemoveAll_FollowsAnchorSymlinkAbove is
// the Bazzite case: a delete target reached via a symlinked HOME
// path. Strict RemoveAll (StrictResolver / legacy
// RemoveAllNoSymlink) rejects at the symlink. UserConfig
// RemoveAll accepts the system symlink above the anchor and
// performs the delete via the real filesystem.
func TestUserConfigResolver_RemoveAll_FollowsAnchorSymlinkAbove(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		// Mirror Bazzite's layout: real HOME at /var/home/user,
		// /home/user is a symlink to it.
		realHome := filepath.Join(base, "var", "home", "user")
		if err := os.MkdirAll(realHome, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(base, "var", "home"), filepath.Join(base, "home")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		linkedHome := filepath.Join(base, "home", "user")
		os.Setenv("HOME", linkedHome)

		// Stage a tree to delete: /home/user/.local/state/stado/worktrees/abc
		target := filepath.Join(linkedHome, ".local", "state", "stado", "worktrees", "abc")
		if err := os.MkdirAll(filepath.Join(target, "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "subdir", "file.txt"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}

		uc := NewUserConfigResolver()
		if err := uc.RemoveAll(target); err != nil {
			t.Fatalf("RemoveAll via symlinked HOME: %v", err)
		}
		// Verify gone via the real path.
		realTarget := filepath.Join(realHome, ".local", "state", "stado", "worktrees", "abc")
		if _, err := os.Stat(realTarget); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target still exists at real path: %v", err)
		}
	})
}

// TestUserConfigResolver_RemoveAll_RejectsFinalSymlink: a
// symlinked target is rejected — removing it would either
// remove the link or follow it and remove off-tree contents.
// Both are wrong.
func TestUserConfigResolver_RemoveAll_RejectsFinalSymlink(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home", "user")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		realDir := filepath.Join(base, "outside")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(home, "redirect")
		if err := os.Symlink(realDir, linkPath); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		uc := NewUserConfigResolver()
		if err := uc.RemoveAll(linkPath); err == nil {
			t.Fatal("expected symlink rejection, got nil")
		}
		// realDir must still exist (not followed).
		if _, err := os.Stat(realDir); err != nil {
			t.Errorf("realDir disappeared (symlink was followed): %v", err)
		}
	})
}

// TestUserConfigResolver_RemoveAll_IdempotentOnMissing: removing
// a non-existent path returns nil, matching the legacy
// RemoveAllNoSymlink contract that callers depend on.
func TestUserConfigResolver_RemoveAll_IdempotentOnMissing(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		uc := NewUserConfigResolver()
		if err := uc.RemoveAll(filepath.Join(home, "never-existed")); err != nil {
			t.Errorf("RemoveAll on missing path: got %v, want nil", err)
		}
	})
}

// TestUserConfigResolver_RemoveAll_RejectsSymlinkBelowAnchor: a
// symlink BELOW the HOME/XDG anchor is rejected even if the
// anchor itself is reached via a system symlink. Defense
// against in-user-space attackers planting redirects.
func TestUserConfigResolver_RemoveAll_RejectsSymlinkBelowAnchor(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home", "user")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		// Plant a symlinked dir BELOW the anchor; target points
		// outside, with content that must NOT be removed.
		outside := filepath.Join(base, "outside-tree")
		if err := os.MkdirAll(filepath.Join(outside, "child"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "child", "important"), []byte("don't delete"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Plant the symlink within the user-config tree:
		stadoDir := filepath.Join(home, ".local", "state", "stado")
		if err := os.MkdirAll(stadoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(stadoDir, "redirect")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		uc := NewUserConfigResolver()
		// Remove a path under the symlinked redirect. Should
		// reject (the redirect itself is a symlink in the
		// below-anchor walk).
		if err := uc.RemoveAll(filepath.Join(stadoDir, "redirect", "child")); err == nil {
			t.Fatal("expected symlink-below-anchor rejection, got nil")
		}
		// Outside contents must still exist.
		if _, err := os.Stat(filepath.Join(outside, "child", "important")); err != nil {
			t.Errorf("outside file removed via symlink redirect: %v", err)
		}
	})
}

// TestUserConfigResolver_RemoveAll_OutsideAnchorFallsBackToStrict:
// for paths outside any HOME/XDG anchor, RemoveAll walks
// strict-no-symlink from `/`, matching legacy
// RemoveAllNoSymlink semantics.
func TestUserConfigResolver_RemoveAll_OutsideAnchorFallsBackToStrict(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		// HOME is set but our delete target is outside it.
		home := filepath.Join(base, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		// Delete target lives entirely outside HOME (no covering
		// anchor) — strict fallback handles it.
		target := filepath.Join(base, "outside", "tree", "leaf")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		uc := NewUserConfigResolver()
		if err := uc.RemoveAll(target); err != nil {
			t.Fatalf("RemoveAll on outside-anchor path: %v", err)
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target still exists: %v", err)
		}
	})
}

// TestUserConfigResolver_OutsideAnchor_SymlinkFallsBackToStrict
// covers the round-A2-final outside-anchor-symlink case: when a
// path falls outside any HOME/XDG anchor, the resolver falls
// back to strict-no-symlink. A symlink in the path chain must
// be rejected (no anchor → no chain-above-anchor leeway).
func TestUserConfigResolver_OutsideAnchor_SymlinkFallsBackToStrict(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		// Path is OUTSIDE any HOME/XDG anchor. Plant a symlink
		// in its chain; strict-fallback should reject it.
		realDir := filepath.Join(base, "real")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "outside-link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		uc := NewUserConfigResolver()
		// path = base/outside-link/file — outside HOME, must
		// fall back to strict no-symlink, which rejects.
		if _, err := uc.OpenRoot(filepath.Join(linkDir, "subdir")); err == nil {
			t.Fatal("expected strict-fallback symlink rejection, got nil")
		}
	})
}

// TestUserConfigResolver_LongestAnchor_DiscriminatesXDGOverHOME
// is a discriminating test: stage the operation such that
// picking HOME (the shorter anchor) instead of XDG_STATE_HOME
// (the longer anchor that covers the path) would surface
// detectably. Exercises round-A2-final's "discriminating
// longest-anchor test" requirement.
//
// The trick: place a symlink BELOW HOME's anchor but ABOVE
// XDG_STATE_HOME's anchor. If the resolver chose HOME, the
// symlink would be in the "below anchor" chain and rejected.
// If the resolver chose XDG_STATE_HOME (the correct longest
// match), the symlink is ABOVE the chosen anchor and accepted.
func TestUserConfigResolver_LongestAnchor_DiscriminatesXDGOverHOME(t *testing.T) {
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home")
		// XDG_STATE_HOME is below home but resolved through a
		// symlink (real path is /var/state-home).
		realState := filepath.Join(base, "var", "state-home")
		if err := os.MkdirAll(realState, 0o755); err != nil {
			t.Fatal(err)
		}
		// Plant the symlink as part of the chain BELOW home.
		stateLink := filepath.Join(home, ".local", "state")
		if err := os.MkdirAll(filepath.Dir(stateLink), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realState, stateLink); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		os.Setenv("HOME", home)
		os.Setenv("XDG_STATE_HOME", stateLink) // points at the symlink

		// A target under XDG_STATE_HOME. If HOME is chosen as
		// the anchor, the .local/state symlink appears in the
		// below-anchor chain → rejected. If XDG_STATE_HOME is
		// chosen (longest covering anchor), the symlink is the
		// anchor itself and accepted.
		target := filepath.Join(stateLink, "stado")

		uc := NewUserConfigResolver()
		if err := uc.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("MkdirAll under XDG anchor (longest): %v", err)
		}
		if _, err := os.Stat(filepath.Join(realState, "stado")); err != nil {
			t.Errorf("dir not created via XDG anchor: %v", err)
		}
	})
}

// ---- Sanity: file does NOT leak when below-anchor symlink rejected ---

func TestUserConfigResolver_NoLeakWhenSymlinkRejected(t *testing.T) {
	// Construction-only sanity to ensure we don't accidentally
	// mutate state during a rejection.
	withIsolatedEnv(t, func() {
		base := t.TempDir()
		home := filepath.Join(base, "home", "user")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Setenv("HOME", home)

		// Plant a symlink BELOW the anchor pointing outside.
		outside := filepath.Join(base, "outside")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(home, "redirect")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		uc := NewUserConfigResolver()
		_, err := uc.OpenRoot(filepath.Join(home, "redirect", "child"))
		if err == nil {
			t.Fatal("expected rejection of symlinked path below anchor")
		}
		// Verify nothing was created via the redirect.
		if _, statErr := os.Stat(filepath.Join(outside, "child")); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("file leaked through rejected symlink: %v", statErr)
		}
	})
}
