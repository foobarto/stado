package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/foobarto/stado/internal/workdirpath"
)

const maxActiveVersionMarkerBytes int64 = 4 << 10

func semverize(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func activeNamespaceMarkerName(namespace string) string {
	sum := sha256.Sum256([]byte(namespace))
	return hex.EncodeToString(sum[:])
}

// ReadActivePackageStoreKey returns the exact selected store row. Corrupt,
// empty, oversized, or unreadable markers fail closed; only a genuinely absent
// marker permits highest-version selection.
func ReadActivePackageStoreKey(pluginsDir, namespace string) (key string, present bool, err error) {
	if strings.TrimSpace(namespace) != namespace || namespace == "" {
		return "", false, errors.New("active package requires a valid source namespace")
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(pluginsDir)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = root.Close() }()
	data, err := workdirpath.NewRootResolver(root).ReadFileLimited(
		filepath.Join(activeMarkerDir, activeNamespaceMarkerName(namespace)), maxActiveVersionMarkerBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	key = strings.TrimSpace(string(data))
	if !validInstalledStoreKey(key) {
		return "", true, errors.New("active package marker contains an invalid store key")
	}
	return key, true, nil
}

// PickActivePackage selects within one exact source namespace. A stale marker
// fails closed for that namespace; otherwise the highest signed package semver
// wins. Candidates from different namespaces are rejected.
func PickActivePackage(pluginsDir, namespace string, candidates []InstalledPackage) (InstalledPackage, bool, error) {
	for _, candidate := range candidates {
		if candidate.Identity.Namespace != namespace {
			return InstalledPackage{}, false, errors.New("active package candidates cross source namespaces")
		}
	}
	marker, present, err := ReadActivePackageStoreKey(pluginsDir, namespace)
	if err != nil {
		return InstalledPackage{}, false, err
	}
	if present {
		for _, candidate := range candidates {
			if candidate.Record.StoreKey == marker {
				return candidate, true, nil
			}
		}
		return InstalledPackage{}, false, fmt.Errorf("active marker for source %s points at missing store key %s", namespace, marker)
	}
	if len(candidates) == 0 {
		return InstalledPackage{}, false, nil
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if semver.Compare(semverize(candidate.Manifest.Version), semverize(best.Manifest.Version)) > 0 {
			best = candidate
		}
	}
	return best, true, nil
}

func WriteActivePackageMarker(pluginsDir string, pkg InstalledPackage) error {
	if filepath.Dir(pkg.Dir) != filepath.Clean(pluginsDir) {
		return errors.New("active package is not inside selected install root")
	}
	if pkg.Record.StoreKey == "" || pkg.Identity.Namespace == "" {
		return errors.New("active package requires verified store identity")
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(pluginsDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	resolver := workdirpath.NewRootResolver(root)
	if err := resolver.MkdirAll(activeMarkerDir, 0o755); err != nil {
		return err
	}
	return resolver.WriteFileAtomic(filepath.Join(activeMarkerDir, activeNamespaceMarkerName(pkg.Identity.Namespace)), []byte(pkg.Record.StoreKey), 0o600)
}

func RemoveActivePackageMarker(pluginsDir, namespace string) error {
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	err = root.Remove(filepath.Join(activeMarkerDir, activeNamespaceMarkerName(namespace)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
