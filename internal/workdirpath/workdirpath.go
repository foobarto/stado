package workdirpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve returns a canonical absolute path confined to workdir.
// Symlinks are resolved before the boundary check so in-repo links to
// out-of-repo targets are rejected. When allowMissing is true, the
// deepest existing ancestor is resolved and the missing suffix is
// appended, which allows safe create/write paths under the workdir.
func Resolve(workdir, path string, allowMissing bool) (string, error) {
	if workdir == "" {
		return "", errors.New("workdir unavailable")
	}
	root, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", err
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)

	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if !allowMissing || !os.IsNotExist(err) {
			return "", err
		}
		dir := filepath.Dir(target)
		suffix := filepath.Base(target)
		for dir != string(filepath.Separator) && dir != "." {
			if _, statErr := os.Stat(dir); statErr == nil {
				break
			}
			dir, suffix = filepath.Dir(dir), filepath.Join(filepath.Base(dir), suffix)
		}
		resolvedDir, derr := filepath.EvalSymlinks(dir)
		if derr != nil {
			return "", derr
		}
		resolved = filepath.Join(resolvedDir, suffix)
	}
	resolved = filepath.Clean(resolved)
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workdir", path)
	}
	return resolved, nil
}

// Glob returns filepath.Glob matches for a workdir-relative pattern
// after rejecting absolute paths and leading `..` escapes.
func Glob(workdir, pattern string) ([]string, error) {
	if workdir == "" {
		return nil, errors.New("workdir unavailable")
	}
	if filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("path %q escapes workdir", pattern)
	}
	clean := filepath.Clean(pattern)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path %q escapes workdir", pattern)
	}
	return filepath.Glob(filepath.Join(workdir, pattern))
}
