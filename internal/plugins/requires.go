package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

// RequireSpec is one parsed entry from Manifest.Requires.
//
// The on-disk format is a single string:
//
//   "http-session"               → name only, any version
//   "http-session >= 0.1.0"      → name + minimum semver
//   "http-session >= v0.1.0"     → equivalent (v-prefix stripped)
//
// The constraint operator MUST be ">=" if present. Pre-1.0 we don't
// support pinned versions or upper bounds — keep the surface narrow
// until a real plugin needs more.
type RequireSpec struct {
	Name       string
	MinVersion string // semver, no v-prefix; "" = any version
}

// ParseRequire parses one requires-list entry. Returns ok=false on
// malformed input (caller decides whether to error or skip).
func ParseRequire(entry string) (RequireSpec, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return RequireSpec{}, fmt.Errorf("plugins: requires entry is empty")
	}
	parts := strings.Fields(entry)
	if len(parts) == 1 {
		return RequireSpec{Name: parts[0]}, nil
	}
	if len(parts) != 3 {
		return RequireSpec{}, fmt.Errorf("plugins: requires %q must be `<name>` or `<name> >= <version>`", entry)
	}
	if parts[1] != ">=" {
		return RequireSpec{}, fmt.Errorf("plugins: requires %q — only `>=` operator supported", entry)
	}
	return RequireSpec{
		Name:       parts[0],
		MinVersion: strings.TrimPrefix(parts[2], "v"),
	}, nil
}

// CheckRequires verifies every entry in m.Requires is satisfied by
// an installed plugin under pluginsDir. Returns a multi-error
// describing all unsatisfied entries (so the operator sees every
// missing dep in one go, not just the first).
func CheckRequires(m *Manifest, pluginsDir string) error {
	if m == nil || len(m.Requires) == 0 {
		return nil
	}
	var unmet []string
	for _, raw := range m.Requires {
		spec, err := ParseRequire(raw)
		if err != nil {
			unmet = append(unmet, err.Error())
			continue
		}
		// Find the highest installed version of spec.Name under pluginsDir.
		ver, ok := highestInstalledVersion(pluginsDir, spec.Name)
		if !ok {
			unmet = append(unmet, fmt.Sprintf("%s: not installed", spec.Name))
			continue
		}
		if spec.MinVersion != "" {
			if !versionAtLeast(ver, spec.MinVersion) {
				unmet = append(unmet, fmt.Sprintf("%s: installed v%s < required v%s", spec.Name, ver, spec.MinVersion))
				continue
			}
		}
	}
	if len(unmet) == 0 {
		return nil
	}
	return fmt.Errorf("plugin requires unmet:\n  - %s\n  install missing plugins first via `stado plugin install <id>`", strings.Join(unmet, "\n  - "))
}

// highestInstalledVersion scans pluginsDir for `<name>-<version>/`
// directories and returns the highest version (no-v form) found.
// Returns ok=false when no version is installed.
func highestInstalledVersion(pluginsDir, name string) (string, bool) {
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return "", false
	}
	prefix := name + "-"
	var best string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		v := strings.TrimPrefix(n, prefix)
		// Skip "active" subdir and friends.
		if v == "" || v == "active" {
			continue
		}
		if !looksLikeVersion(v) {
			continue
		}
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// looksLikeVersion accepts "0.1.0", "0.1.0-rc1", "v0.1.0", etc.
func looksLikeVersion(v string) bool {
	if v == "" {
		return false
	}
	c := v[0]
	if c == 'v' && len(v) > 1 {
		c = v[1]
	}
	return c >= '0' && c <= '9'
}

// versionAtLeast returns true when actual >= required (semver compare,
// v-prefix tolerant).
func versionAtLeast(actual, required string) bool {
	return compareVersions(actual, required) >= 0
}

func compareVersions(a, b string) int {
	if !strings.HasPrefix(a, "v") {
		a = "v" + a
	}
	if !strings.HasPrefix(b, "v") {
		b = "v" + b
	}
	if !semver.IsValid(a) || !semver.IsValid(b) {
		// Not semver — fall back to string compare.
		return strings.Compare(a, b)
	}
	return semver.Compare(a, b)
}

// requiresPath is a small convenience for callers that want
// pluginsDir from a state-dir.
func requiresPath(stateDir string) string {
	return filepath.Join(stateDir, "plugins")
}
