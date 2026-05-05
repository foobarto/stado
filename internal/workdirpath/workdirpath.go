package workdirpath

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	maxGlobWalkEntries = 200000
	maxGlobWalkDepth   = 128
	maxGlobStored      = 10000
	globReadDirBatch   = 128
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
	// Canonicalise to absolute BEFORE EvalSymlinks. Go 1.25+
	// changed EvalSymlinks to preserve relative-input shape on
	// output; the prefix-confinement check below assumes root is
	// absolute, otherwise a relative resolved path that's truly
	// under workdir gets misidentified as an escape.
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(absWorkdir)
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
	// See Resolve — Go 1.25+ preserves EvalSymlinks input shape;
	// canonicalise to absolute first so the Rel below works.
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(absWorkdir)
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

// ReadRegularFileNoSymlinkLimited reads an absolute or relative filesystem path
// while rejecting symlinked directory components, symlinked final paths, and
// files larger than maxBytes.
func ReadRegularFileNoSymlinkLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := OpenRegularFileNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if maxBytes < 0 {
		maxBytes = 0
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, path)
	}
	return data, nil
}

// ReadRegularFileUnderUserConfigLimited reads path with the same trust-anchor
// model as MkdirAllUnderUserConfig / OpenRootUnderUserConfig: the chain UP
// TO the longest HOME / XDG_*_HOME ancestor is treated as operator-supplied
// (so a system-level `/home → /var/home` symlink is accepted), and the
// remainder is walked with strict no-symlink enforcement. When path falls
// outside any anchor, falls back to the strict from-/ ReadRegularFileNoSymlinkLimited.
func ReadRegularFileUnderUserConfigLimited(path string, maxBytes int64) ([]byte, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	anchor := userTrustAnchor(abs)
	if anchor == "" {
		return ReadRegularFileNoSymlinkLimited(abs, maxBytes)
	}
	rel, err := filepath.Rel(anchor, abs)
	if err != nil {
		return nil, err
	}
	if rel == "." || rel == "" {
		return nil, fmt.Errorf("invalid file path: %s (equal to user-config anchor)", path)
	}
	root, err := OpenRootNoSymlinkUnder(anchor, filepath.Dir(rel))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return ReadRootRegularFileLimited(root, filepath.Base(rel), maxBytes)
}

// ReadRootRegularFileLimited opens a regular file relative to root, rejects
// final symlinks and open-time swaps, and reads at most maxBytes.
func ReadRootRegularFileLimited(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	if root == nil {
		return nil, errors.New("root unavailable")
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("file is a symlink: %s", name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular: %s", name)
	}

	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular: %s", name)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while opening: %s", name)
	}
	if openedInfo.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, name)
	}
	return data, nil
}

// OpenRegularFileUnderUserConfig is the OpenRegularFileNoSymlink analogue
// for the trust-anchor walk: directory components UP TO the longest
// HOME / XDG_*_HOME ancestor are accepted as the operator's environment
// (so `/home → /var/home` on Atomic Fedora / Bazzite resolves), the rest
// of the path is walked with strict no-symlink enforcement, and the
// final component must be a regular file (no terminal symlink). When
// path falls outside any anchor, falls back to OpenRegularFileNoSymlink.
func OpenRegularFileUnderUserConfig(path string) (*os.File, error) {
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
	root, err := OpenRootUnderUserConfig(filepath.Dir(abs))
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

// ReadRegularFileUnderUserConfigNoLimit is a convenience wrapper that reads
// the entire file via OpenRegularFileUnderUserConfig.
func ReadRegularFileUnderUserConfigNoLimit(path string) ([]byte, error) {
	f, err := OpenRegularFileUnderUserConfig(path)
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

// MkdirAllUnderUserConfig creates path with no-symlink enforcement
// anchored at the operator's HOME / XDG_*_HOME trust ancestors.
//
// When path falls under one of those anchors, the chain UP TO the
// anchor is treated as operator-supplied: missing components are
// created via os.MkdirAll (no symlink rejection — `/home` may
// legitimately symlink to `/var/home` on Atomic Fedora / Silverblue,
// `/var/home` may not exist yet on a fresh container, etc.). The
// rest of the path (anchor → leaf) is walked with the strict
// no-symlink check, so an in-user-space attacker can't redirect a
// stado write by planting a symlink under the anchor.
//
// When path is NOT under any anchor, we fall back to the strict
// from-`/` MkdirAllNoSymlink check — preserving the defense for
// callers that operate on adversarially-supplied paths (in-tmpdir
// tests, in-repo writes).
//
// Use this for HOME-rooted system paths (XDG config / data / state
// dirs, the audit-key directory, the per-user worktree root). See
// EP-0028 for the threat-model rationale.
func MkdirAllUnderUserConfig(path string, perm os.FileMode) error {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	anchor := userTrustAnchor(abs)
	if anchor == "" {
		return MkdirAllNoSymlink(abs, perm)
	}
	// Ensure the anchor exists. The chain UP TO the anchor is the
	// operator's environment, not adversarial.
	if info, err := os.Stat(anchor); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(anchor, perm); err != nil {
			return err
		}
	} else if !info.IsDir() {
		return fmt.Errorf("user-config anchor is not a directory: %s", anchor)
	}
	rel, err := filepath.Rel(anchor, abs)
	if err != nil {
		return err
	}
	if rel == "." || rel == "" {
		return nil
	}
	return MkdirAllNoSymlinkUnder(anchor, rel, perm)
}

// OpenRootUnderUserConfig opens path with the same trust-anchor
// model as MkdirAllUnderUserConfig. The anchor itself must already
// exist (Open doesn't create); paths under the anchor are walked
// with no-symlink enforcement.
func OpenRootUnderUserConfig(path string) (*os.Root, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	anchor := userTrustAnchor(abs)
	if anchor == "" {
		return OpenRootNoSymlink(abs)
	}
	rel, err := filepath.Rel(anchor, abs)
	if err != nil {
		return nil, err
	}
	if rel == "." || rel == "" {
		return os.OpenRoot(anchor)
	}
	return OpenRootNoSymlinkUnder(anchor, rel)
}

// userTrustAnchor returns the longest HOME/XDG_*_HOME ancestor that
// covers absPath, or "" when none does. We treat these as
// operator-controlled trust roots: their own ancestor symlink chain
// is whatever the OS gave us; we don't second-guess the user's
// system layout. Paths below them are still subject to no-symlink
// enforcement against in-user-space attackers.
func userTrustAnchor(absPath string) string {
	var candidates []string
	for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if v := os.Getenv(env); v != "" {
			candidates = append(candidates, v)
		}
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		candidates = append(candidates, h)
	}
	var best string
	for _, c := range candidates {
		ca, err := filepath.Abs(filepath.Clean(c))
		if err != nil {
			continue
		}
		if absPath != ca && !strings.HasPrefix(absPath+string(filepath.Separator), ca+string(filepath.Separator)) {
			continue
		}
		if len(ca) > len(best) {
			best = ca
		}
	}
	return best
}

// MkdirAllNoSymlinkUnder ensures relSub exists as a directory tree
// rooted under ancestor. ancestor must be an existing directory and
// is opened via os.OpenRoot (which follows the OS path resolution
// up to and including ancestor — symlinks IN the ancestor's own
// path chain are accepted as the operator's environment, e.g.
// `/home` symlinked to `/var/home` on Fedora Atomic / Silverblue).
// Symlinks in path components UNDER ancestor are still rejected,
// matching the threat model of MkdirAllNoSymlink.
//
// Use this for HOME-rooted system paths (XDG config / data / state
// dirs, the user's audit-key directory, the per-user worktree root)
// where the strict from-/ walk fails on systems whose `/home` is a
// symlink. For genuinely untrusted writes (in-repo paths supplied
// by an adversary, plugin sandbox writes), keep using
// MkdirAllNoSymlink so the entire path chain is checked.
func MkdirAllNoSymlinkUnder(ancestor, relSub string, perm os.FileMode) error {
	if strings.Contains(ancestor, "\x00") || strings.Contains(relSub, "\x00") {
		return fmt.Errorf("invalid directory path %q / %q", ancestor, relSub)
	}
	if ancestor == "" {
		return fmt.Errorf("ancestor required")
	}
	root, err := os.OpenRoot(ancestor)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return MkdirAllRootNoSymlink(root, filepath.Clean(relSub), perm)
}

// OpenRootNoSymlinkUnder is the OpenRootNoSymlink analogue for a
// trusted-ancestor walk. See MkdirAllNoSymlinkUnder.
func OpenRootNoSymlinkUnder(ancestor, relSub string) (*os.Root, error) {
	if strings.Contains(ancestor, "\x00") || strings.Contains(relSub, "\x00") {
		return nil, fmt.Errorf("invalid directory path %q / %q", ancestor, relSub)
	}
	if ancestor == "" {
		return nil, fmt.Errorf("ancestor required")
	}
	cur, err := os.OpenRoot(ancestor)
	if err != nil {
		return nil, err
	}
	rel := filepath.Clean(relSub)
	if rel == "." || rel == "" {
		return cur, nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = cur.Close()
			return nil, fmt.Errorf("invalid directory path %q", relSub)
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
	// See Resolve — Go 1.25+ preserves EvalSymlinks input shape;
	// canonicalise to absolute first so the Rel below works.
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(absWorkdir)
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

// Glob returns bounded matches for a workdir-relative pattern after rejecting
// absolute paths and leading `..` escapes.
func Glob(workdir, pattern string) ([]string, error) {
	matches, _, err := GlobLimited(workdir, pattern, maxGlobStored)
	return matches, err
}

// GlobLimited returns at most maxStored matches plus the total match count.
// Traversal is rooted, skips symlinked directories, and fails if the worktree
// exceeds the glob entry or depth budget.
func GlobLimited(workdir, pattern string, maxStored int) ([]string, int, error) {
	return globLimited(workdir, pattern, maxStored, defaultGlobLimits())
}

type globLimits struct {
	maxEntries int
	maxDepth   int
}

type globState struct {
	rootPath  string
	pattern   string
	maxStored int
	globLimits
	entries int
	matches []string
	total   int
}

func defaultGlobLimits() globLimits {
	return globLimits{maxEntries: maxGlobWalkEntries, maxDepth: maxGlobWalkDepth}
}

func globLimited(workdir, pattern string, maxStored int, limits globLimits) ([]string, int, error) {
	if workdir == "" {
		return nil, 0, errors.New("workdir unavailable")
	}
	if maxStored < 0 {
		maxStored = 0
	}
	if filepath.IsAbs(pattern) {
		return nil, 0, fmt.Errorf("path %q escapes workdir", pattern)
	}
	if strings.Contains(pattern, "\x00") {
		return nil, 0, fmt.Errorf("invalid glob pattern %q", pattern)
	}
	clean := filepath.Clean(pattern)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, 0, fmt.Errorf("path %q escapes workdir", pattern)
	}
	if _, err := filepath.Match(clean, ""); err != nil {
		return nil, 0, err
	}
	rootPath, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return nil, 0, err
	}
	root, err := OpenRootNoSymlink(rootPath)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = root.Close() }()

	state := &globState{
		rootPath:   rootPath,
		pattern:    clean,
		maxStored:  maxStored,
		globLimits: limits,
		matches:    make([]string, 0, min(maxStored, 64)),
	}
	if err := walkGlobDir(root, ".", state, 0); err != nil {
		return nil, 0, err
	}
	return state.matches, state.total, nil
}

func walkGlobDir(root *os.Root, rel string, state *globState, depth int) error {
	if depth > state.maxDepth {
		return fmt.Errorf("glob walk exceeded max depth %d", state.maxDepth)
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil
	}
	dir, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	openedInfo, err := dir.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.IsDir() {
		return fmt.Errorf("glob walk path is not a directory: %s", filepath.ToSlash(rel))
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("glob walk directory changed while opening: %s", filepath.ToSlash(rel))
	}
	names, err := readGlobDirNames(dir, state)
	if err != nil {
		return err
	}
	for _, name := range names {
		childRel := name
		if rel != "." {
			childRel = filepath.Join(rel, name)
		}
		if err := visitGlobPath(root, childRel, state, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func readGlobDirNames(dir *os.File, state *globState) ([]string, error) {
	var names []string
	for {
		entries, readErr := dir.ReadDir(globReadDirBatch)
		for _, entry := range entries {
			state.entries++
			if state.entries > state.maxEntries {
				return nil, fmt.Errorf("glob walk contains more than %d entries", state.maxEntries)
			}
			name := entry.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("glob walk invalid entry name %q", name)
			}
			names = append(names, name)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Strings(names)
	return names, nil
}

func visitGlobPath(root *os.Root, rel string, state *globState, depth int) error {
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if ok, err := filepath.Match(state.pattern, rel); err != nil {
		return err
	} else if ok {
		state.total++
		if len(state.matches) < state.maxStored {
			state.matches = append(state.matches, filepath.Join(state.rootPath, rel))
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil
	}
	return walkGlobDir(root, rel, state, depth)
}
