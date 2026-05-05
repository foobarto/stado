package plugins

import (
	"os"
	"path/filepath"
)

// DevSentinelVersion is the version string used by `stado plugin dev
// --watch` for the in-development install. The dev install lives at
// <state>/plugins/<name>-0.0.0-dev/ and is pinned via the active-
// marker mechanism so the unified registry registers it as the
// active version. Cleanup removes both on watch-loop exit.
const DevSentinelVersion = "0.0.0-dev"

// PinActiveDev writes the active-version marker for `name` pointing
// at DevSentinelVersion. Caller is responsible for ensuring the
// install dir exists before any registry lookup happens.
func PinActiveDev(stateDir, name string) error {
	activeDir := filepath.Join(stateDir, "plugins", "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(activeDir, name), []byte(DevSentinelVersion), 0o644)
}

// CleanupDev removes the dev install dir and the active-version
// marker for `name`. Idempotent: missing dir or marker is not an
// error. Called on watch-loop exit (defer + signal handler).
func CleanupDev(stateDir, name string) error {
	installDir := filepath.Join(stateDir, "plugins", name+"-"+DevSentinelVersion)
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	markerPath := filepath.Join(stateDir, "plugins", "active", name)
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
