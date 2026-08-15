package plugins

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/workdirpath"
)

// DevSentinelVersion is the version string used by `stado plugin dev
// --watch` for the in-development install. The dev install lives in its
// source-keyed store directory and is selected by the exact active-package
// marker. Cleanup removes every rebuild for that source on watch-loop exit.
const DevSentinelVersion = "0.0.0-dev"

// activeMarkerDir is the reserved subdirectory under <state>/plugins/ that
// holds active-version markers (one file per pinned plugin). It is NOT an
// installed plugin: enumeration of installed plugins must skip it, or a
// phantom "active" row appears in `plugin list` with an inflated count.
// Defined here, at the producer, so every consumer (installed.go, requires.go)
// shares one source of truth and a rename can't silently break the skips.
const activeMarkerDir = "active"

// CleanupDev removes every dev-sentinel package belonging to one exact local
// source path plus its source-keyed active marker. Display aliases are never
// consulted, so repeated same-version rebuilds remain unambiguous.
func CleanupDev(stateDir, source string) error {
	pluginsDir := filepath.Join(stateDir, "plugins")
	canonical, err := canonicalLocalSource(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	packages, err := ListInstalledPackages(pluginsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	namespace := ""
	for _, pkg := range packages {
		if pkg.Record.Kind != InstallLocal || pkg.Record.CanonicalSource != canonical || pkg.Manifest.Version != DevSentinelVersion {
			continue
		}
		namespace = pkg.Identity.Namespace
		if err := workdirpath.NewUserConfigResolver().RemoveAll(pkg.Dir); err != nil {
			return err
		}
	}
	if namespace != "" {
		return RemoveActivePackageMarker(pluginsDir, namespace)
	}
	return nil
}
