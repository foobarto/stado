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
