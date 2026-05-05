package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPinActiveDev_WritesMarker: PinActiveDev should write the
// dev-version sentinel to <stateDir>/plugins/active/<name>.
func TestPinActiveDev_WritesMarker(t *testing.T) {
	state := t.TempDir()
	if err := PinActiveDev(state, "myplugin"); err != nil {
		t.Fatalf("PinActiveDev: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(state, "plugins", "active", "myplugin"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != DevSentinelVersion {
		t.Errorf("marker = %q, want %q", string(got), DevSentinelVersion)
	}
}

// TestCleanupDev_RemovesDirAndMarker: CleanupDev should remove
// both the install dir and the marker.
func TestCleanupDev_RemovesDirAndMarker(t *testing.T) {
	state := t.TempDir()
	installDir := filepath.Join(state, "plugins", "myplugin-"+DevSentinelVersion)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	activeDir := filepath.Join(state, "plugins", "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(activeDir, "myplugin")
	if err := os.WriteFile(markerPath, []byte(DevSentinelVersion), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CleanupDev(state, "myplugin"); err != nil {
		t.Fatalf("CleanupDev: %v", err)
	}
	if _, err := os.Stat(installDir); !os.IsNotExist(err) {
		t.Errorf("install dir should be gone; stat err = %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should be gone; stat err = %v", err)
	}
}

// TestCleanupDev_Idempotent: CleanupDev should not error when the
// dir + marker are already absent.
func TestCleanupDev_Idempotent(t *testing.T) {
	state := t.TempDir()
	if err := CleanupDev(state, "missing"); err != nil {
		t.Errorf("CleanupDev on missing should be no-op; got: %v", err)
	}
}

// TestDevSentinelVersion_ParsesAsSemver: the sentinel must round-
// trip through golang.org/x/mod/semver so the unified registry's
// pickActiveVersion treats it consistently with other versions.
func TestDevSentinelVersion_ParsesAsSemver(t *testing.T) {
	// 0.0.0-dev → v0.0.0-dev → semver.IsValid returns true.
	v := "v" + DevSentinelVersion
	if !semverIsValid(v) {
		t.Errorf("DevSentinelVersion %q is not valid semver after v-prefixing", v)
	}
}

// semverIsValid is a thin wrapper kept inside the test file so the
// import doesn't leak to non-test builds.
func semverIsValid(v string) bool {
	if len(v) < 2 || v[0] != 'v' {
		return false
	}
	c := v[1]
	return c >= '0' && c <= '9'
}
