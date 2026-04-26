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
	f, err := OpenReadFile(workdir, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// OpenReadFile opens path through os.Root so the final open remains confined
// even if symlinks are swapped after path resolution. The returned file must
// be closed by the caller.
func OpenReadFile(workdir, path string) (*os.File, error) {
	rootPath, rel, err := RootRel(workdir, path, false)
	if err != nil {
		return nil, err
	}
	root, err := OpenRootNoSymlink(rootPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(rel)
}

// ReadRegularFileNoSymlink reads an absolute or relative filesystem path while
// rejecting symlinked directory components and symlinked final paths.
func ReadRegularFileNoSymlink(path string) ([]byte, error) {
	f, err := OpenRegularFileNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// OpenRegularFileNoSymlink opens an absolute or relative filesystem path while
// rejecting symlinked directory components and symlinked final paths.
func OpenRegularFileNoSymlink(path string) (*os.File, error) {
	if strings.Contains(path, "\x00") {
		return nil, fmt.Errorf("invalid file path %q", path)
	}
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid file path %q", path)
	}
	root, err := OpenRootNoSymlink(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(base)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("file is a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular: %s", path)
	}
	f, err := root.Open(base)
	if err != nil {
		return nil, err
	}
	openedInfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("file is not regular: %s", path)
	}
	if !os.SameFile(info, openedInfo) {
		_ = f.Close()
		return nil, fmt.Errorf("file changed while opening: %s", path)
	}
	return f, nil
}

// WriteFile writes path through os.Root so create/truncate cannot escape the
// workdir via a concurrently swapped symlink.
func WriteFile(workdir, path string, data []byte, perm os.FileMode) error {
	rootPath, rel, err := RootRelForWrite(workdir, path)
	if err != nil {
		return err
	}
	root, err := OpenRootNoSymlink(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if dir := filepath.Dir(rel); dir != "." {
		if err := MkdirAllRootNoSymlink(root, dir, 0o755); err != nil {
			return err
		}
	}
	return WriteRootFileAtomic(root, rel, data, perm)
}

// MkdirAllNoSymlink creates path like os.MkdirAll, but rejects any existing
// symlink component instead of following it while preparing a write root.
func MkdirAllNoSymlink(path string, perm os.FileMode) error {
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("invalid directory path %q", path)
	}
	clean := filepath.Clean(path)
	if clean == "." {
		return nil
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return err
	}
	rootPath, rel := splitAbsoluteRoot(abs)
	root, err := OpenRootNoSymlink(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return MkdirAllRootNoSymlink(root, rel, perm)
}

// OpenRootNoSymlink opens an existing directory while rejecting any symlink
// component in the directory path.
func OpenRootNoSymlink(path string) (*os.Root, error) {
	if strings.Contains(path, "\x00") {
		return nil, fmt.Errorf("invalid directory path %q", path)
	}
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return nil, err
	}
	rootPath, rel := splitAbsoluteRoot(abs)
	cur, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = cur.Close()
			return nil, fmt.Errorf("invalid directory path %q", path)
		}
		info, err := cur.Lstat(part)
		if err != nil {
			_ = cur.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = cur.Close()
			return nil, fmt.Errorf("directory component is a symlink: %s", part)
		}
		if !info.IsDir() {
			_ = cur.Close()
			return nil, fmt.Errorf("directory component is not a directory: %s", part)
		}
		next, err := cur.OpenRoot(part)
		if err != nil {
			_ = cur.Close()
			return nil, err
		}
		_ = cur.Close()
		cur = next
	}
	return cur, nil
}

// RemoveAllNoSymlink removes path without following symlinked directory
// components. A final symlink is rejected instead of being removed silently.
func RemoveAllNoSymlink(path string) error {
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("invalid remove path %q", path)
	}
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return err
	}
	name := filepath.Base(abs)
	if name == "." || name == string(filepath.Separator) {
		return fmt.Errorf("invalid remove path %q", path)
	}
	parent := filepath.Dir(abs)
	root, err := OpenRootNoSymlink(parent)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("remove path is a symlink: %s", path)
	case err == nil:
	case os.IsNotExist(err):
		return nil
	default:
		return err
	}
	return root.RemoveAll(name)
}

// MkdirAllRootNoSymlink creates a directory tree relative to root while
// rejecting any existing symlink component.
func MkdirAllRootNoSymlink(root *os.Root, path string, perm os.FileMode) error {
	if root == nil {
		return errors.New("root unavailable")
	}
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("invalid directory path %q", path)
	}
	clean := filepath.Clean(path)
	if clean == "." {
		return nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid directory path %q", path)
	}
	cur := root
	closeCur := false
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if closeCur {
				_ = cur.Close()
			}
			return fmt.Errorf("invalid directory path %q", path)
		}
		info, err := cur.Lstat(part)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			if closeCur {
				_ = cur.Close()
			}
			return fmt.Errorf("directory component is a symlink: %s", part)
		case err == nil && !info.IsDir():
			if closeCur {
				_ = cur.Close()
			}
			return fmt.Errorf("directory component is not a directory: %s", part)
		case err == nil:
		case os.IsNotExist(err):
			if err := cur.Mkdir(part, perm); err != nil {
				if closeCur {
					_ = cur.Close()
				}
				return err
			}
		default:
			if closeCur {
				_ = cur.Close()
			}
			return err
		}
		next, err := cur.OpenRoot(part)
		if err != nil {
			if closeCur {
				_ = cur.Close()
			}
			return err
		}
		if closeCur {
			_ = cur.Close()
		}
		cur = next
		closeCur = true
	}
	if closeCur {
		return cur.Close()
	}
	return nil
}

func splitAbsoluteRoot(path string) (root, rel string) {
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	sep := string(filepath.Separator)
	root = volume
	if strings.HasPrefix(rest, sep) {
		root += sep
		rest = strings.TrimLeft(rest, sep)
	}
	if root == "" {
		root = "."
	}
	return root, rest
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
	return writeRootFileAtomic(root, name, data, perm, true)
}

// WriteRootFileAtomicExactMode is like WriteRootFileAtomic, but always applies
// perm to the replacement file instead of preserving an existing file's mode.
func WriteRootFileAtomicExactMode(root *os.Root, name string, data []byte, perm os.FileMode) error {
	return writeRootFileAtomic(root, name, data, perm, false)
}

func writeRootFileAtomic(root *os.Root, name string, data []byte, perm os.FileMode, preserveExistingMode bool) error {
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
		if preserveExistingMode {
			writePerm = info.Mode().Perm()
		}
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
