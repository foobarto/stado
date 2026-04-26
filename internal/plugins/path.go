package plugins

import (
	"fmt"
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
