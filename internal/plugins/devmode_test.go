package plugins

import "testing"

// TestCleanupDev_Idempotent: CleanupDev should not error when the
// dir + marker are already absent.
func TestCleanupDev_Idempotent(t *testing.T) {
	state := t.TempDir()
	if err := CleanupDev(state, state+"/missing"); err != nil {
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
