package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstalledDir resolves an installed plugin ID below the caller's plugin root.
// IDs are single directory names like "auto-compact-0.1.0"; separators and
// traversal are rejected so callers can't escape the plugins directory.
func InstalledDir(root, id string) (string, error) {
	if id == "" || id == "." || !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("invalid plugin id %q", id)
	}
	return filepath.Join(root, id), nil
}

// InstalledDirInAny searches roots in order and returns the path of the
// first root that contains a directory for the given plugin id. When no
// root has the plugin, returns the path in roots[0] so the caller's
// existing ErrNotExist handling stays intact. EP-0035.
func InstalledDirInAny(roots []string, id string) (string, error) {
	if id == "" || id == "." || !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("invalid plugin id %q", id)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("no plugin roots configured")
	}
	for _, root := range roots {
		dir := filepath.Join(root, id)
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}
	// Fall through to the primary (first) root so callers get a sensible
	// ErrNotExist when the plugin truly isn't installed anywhere.
	return filepath.Join(roots[0], id), nil
}
