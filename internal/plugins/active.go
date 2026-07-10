package plugins

import (
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/foobarto/stado/internal/workdirpath"
)

const maxActiveVersionMarkerBytes int64 = 4 << 10

// ActiveVersionMarker reads the per-plugin marker beneath one plugin root.
// Invalid names, missing markers, symlinks, and read failures yield "" so the
// caller uses normal highest-version selection.
func ActiveVersionMarker(pluginsDir, pluginName string) string {
	if _, err := InstalledDir(".", pluginName); err != nil {
		return ""
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(pluginsDir)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	data, err := workdirpath.NewRootResolver(root).ReadFileLimited(
		filepath.Join(activeMarkerDir, pluginName), maxActiveVersionMarkerBytes)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// PickActiveVersion mirrors runtime plugin selection for one plugin root:
// honor a valid marker, otherwise choose the highest semantic version.
func PickActiveVersion(pluginsDir, pluginName string, candidates []string) string {
	if marker := ActiveVersionMarker(pluginsDir, pluginName); marker != "" {
		normMarker := semverize(marker)
		for _, version := range candidates {
			if semverize(version) == normMarker {
				return version
			}
		}
		return ""
	}
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, version := range candidates[1:] {
		if semver.Compare(semverize(version), semverize(best)) > 0 {
			best = version
		}
	}
	return best
}

// SplitInstalledID splits <name>-<version> using the directory convention
// shared by runtime discovery and dependency validation.
func SplitInstalledID(id string) (name, version string, ok bool) {
	for i := len(id) - 1; i >= 1; i-- {
		if id[i] != '-' {
			continue
		}
		rest := id[i+1:]
		if len(rest) == 0 {
			continue
		}
		switch {
		case rest[0] >= '0' && rest[0] <= '9':
			return id[:i], rest, true
		case rest[0] == 'v' && len(rest) >= 2 && rest[1] >= '0' && rest[1] <= '9':
			return id[:i], rest, true
		}
	}
	return "", "", false
}

func semverize(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}
