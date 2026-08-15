package plugins

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstalledDir resolves one exact source-derived store key below a plugin root.
// Display names and name-version aliases are intentionally rejected.
func InstalledDir(root, storeKey string) (string, error) {
	if !validInstalledStoreKey(storeKey) {
		return "", fmt.Errorf("invalid installed plugin store key %q", storeKey)
	}
	return filepath.Join(root, storeKey), nil
}

// InstalledDirInAny searches roots in order and returns the path of the
// first root that contains a directory for the given plugin id. When no
// root has the plugin, returns the path in roots[0] so the caller's
// existing ErrNotExist handling stays intact. EP-0035.
func InstalledDirInAny(roots []string, storeKey string) (string, error) {
	if !validInstalledStoreKey(storeKey) {
		return "", fmt.Errorf("invalid installed plugin store key %q", storeKey)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("no plugin roots configured")
	}
	for _, root := range roots {
		dir := filepath.Join(root, storeKey)
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}
	// Fall through to the primary (first) root so callers get a sensible
	// ErrNotExist when the plugin truly isn't installed anywhere.
	return filepath.Join(roots[0], storeKey), nil
}

func validInstalledStoreKey(key string) bool {
	prefix := remoteInstallKeyPrefix
	if strings.HasPrefix(key, localInstallKeyPrefix) {
		prefix = localInstallKeyPrefix
	}
	hexPart := strings.TrimPrefix(key, prefix)
	if len(hexPart) != 64 || len(key) != len(prefix)+64 || !filepath.IsLocal(key) || strings.ContainsAny(key, `/\`) {
		return false
	}
	_, err := hex.DecodeString(hexPart)
	return err == nil
}
