package plugins

import (
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/version"
	"golang.org/x/mod/semver"
)

// CheckMinimumStadoVersion applies the signed host-version gate. Development,
// malformed, and prerelease host identifiers cannot prove compatibility with
// a declared stable minimum and therefore fail closed.
func CheckMinimumStadoVersion(minimum, current string) error {
	if minimum == "" {
		return nil
	}
	if strings.TrimSpace(minimum) != minimum {
		return fmt.Errorf("min_stado_version %q is malformed", minimum)
	}
	minimum = semverize(minimum)
	if !semver.IsValid(minimum) || semver.Prerelease(minimum) != "" {
		return fmt.Errorf("min_stado_version %q must be a stable semantic version", strings.TrimPrefix(minimum, "v"))
	}
	if strings.TrimSpace(current) != current {
		return fmt.Errorf("host stado version %q is not a stable semantic version; cannot satisfy min_stado_version", current)
	}
	current = semverize(current)
	if !semver.IsValid(current) || semver.Prerelease(current) != "" || current == "v0.0.0-dev" {
		return fmt.Errorf("host stado version %q is not a stable semantic version; cannot satisfy min_stado_version", strings.TrimPrefix(current, "v"))
	}
	if semver.Compare(current, minimum) < 0 {
		return fmt.Errorf("plugin requires stado >= %s; host is %s", strings.TrimPrefix(minimum, "v"), strings.TrimPrefix(current, "v"))
	}
	return nil
}

// CheckManifestHostVersion checks the current build identity. Install and
// every runtime reopen call this independently; successful installation on an
// older binary must not remain authoritative after moving state between hosts.
func CheckManifestHostVersion(manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("plugin manifest is nil")
	}
	return CheckMinimumStadoVersion(manifest.MinStadoVersion, version.Version)
}
