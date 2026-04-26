package workdirpath

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
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

// RootRel returns a path suitable for os.Root methods after applying the same
// symlink-aware workdir confinement as Resolve. The returned root is the
// canonical workdir path and rel is relative to that root.
func RootRel(workdir, path string, allowMissing bool) (root, rel string, err error) {
	if workdir == "" {
		return "", "", errors.New("workdir unavailable")
	}
	root, err = filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", "", err
	}
	resolved, err := Resolve(workdir, path, allowMissing)
	if err != nil {
		return "", "", err
	}
	rel, err = filepath.Rel(root, resolved)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes workdir", path)
	}
	return root, rel, nil
}

// ReadFile opens path through os.Root so the final open remains confined even
// if symlinks are swapped after path resolution.
func ReadFile(workdir, path string) ([]byte, error) {
	rootPath, rel, err := RootRel(workdir, path, false)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

// WriteFile writes path through os.Root so create/truncate cannot escape the
// workdir via a concurrently swapped symlink.
func WriteFile(workdir, path string, data []byte, perm os.FileMode) error {
	rootPath, rel, err := RootRelForWrite(workdir, path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return WriteRootFileAtomic(root, rel, data, perm)
}

// RootRelForWrite returns a root-relative write target after resolving parent
// symlinks without following the final path component.
func RootRelForWrite(workdir, path string) (root, rel string, err error) {
	if workdir == "" {
		return "", "", errors.New("workdir unavailable")
	}
	if strings.Contains(path, "\x00") {
		return "", "", fmt.Errorf("path %q contains NUL", path)
	}
	root, err = filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", "", err
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	if target == root {
		return root, ".", nil
	}
	parent := filepath.Dir(target)
	base := filepath.Base(target)
	resolvedParent, err := Resolve(workdir, parent, true)
	if err != nil {
		return "", "", err
	}
	relParent, err := filepath.Rel(root, resolvedParent)
	if err != nil {
		return "", "", err
	}
	if filepath.IsAbs(relParent) || relParent == ".." || strings.HasPrefix(relParent, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes workdir", path)
	}
	if relParent == "." {
		rel = base
	} else {
		rel = filepath.Join(relParent, base)
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes workdir", path)
	}
	return root, rel, nil
}

// WriteRootFileAtomic writes a confined os.Root-relative file through a
// same-directory random temp file and renames it over the final path. Existing
// symlink and non-regular final paths are rejected instead of being followed.
func WriteRootFileAtomic(root *os.Root, name string, data []byte, perm os.FileMode) error {
	if root == nil {
		return errors.New("root unavailable")
	}
	writePerm := perm
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file is not regular: %s", name)
		}
		writePerm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	dir, base := filepath.Split(name)
	tmp := filepath.Join(dir, "."+base+"."+uuid.NewString()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, writePerm)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = root.Remove(tmp)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	renamed = true
	return nil
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
