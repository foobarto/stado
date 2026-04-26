package git

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	maxMaterializedBlobBytes          int64 = maxTreeBlobBytes
	maxMaterializedSymlinkTargetBytes int64 = 4 << 10
)

// MaterializeTreeToDir writes every blob in the given tree into dir, creating
// subdirectories as needed. Symlinks are recreated. Existing files that
// aren't in the tree are left alone (non-destructive by default); call
// MaterializeTreeReplacing for destructive synchronisation.
//
// Inverse of BuildTreeFromDir — round-trips for deterministic Go-in-Go-out.
func (s *Session) MaterializeTreeToDir(treeHash plumbing.Hash, dir string) error {
	return s.materialize(treeHash, dir, false)
}

// MaterializeTreeReplacing is like MaterializeTreeToDir but removes any file
// in dir that isn't represented in the tree. Use for `revert` / `fork`
// semantics where you want the worktree to exactly mirror the ref.
func (s *Session) MaterializeTreeReplacing(treeHash plumbing.Hash, dir string) error {
	return s.materialize(treeHash, dir, true)
}

func (s *Session) materialize(treeHash plumbing.Hash, dir string, replacing bool) error {
	if treeHash.IsZero() {
		if replacing {
			// Zero tree = empty tree. Wipe dir content but keep the dir itself.
			return wipeDir(dir)
		}
		return nil
	}
	tree, err := object.GetTree(s.Sidecar.repo.Storer, treeHash)
	if err != nil {
		return fmt.Errorf("materialize: read tree %s: %w", treeHash, err)
	}
	if err := workdirpath.MkdirAllNoSymlink(dir, 0o750); err != nil {
		return fmt.Errorf("materialize: mkdir %s: %w", dir, err)
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return fmt.Errorf("materialize: root %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	// Track which paths we wrote so we can prune stale files when replacing.
	kept := map[string]bool{}
	if err := s.writeTreeInto(tree, root, dir, dir, kept); err != nil {
		return err
	}

	if replacing {
		return pruneExtras(root, dir, kept)
	}
	return nil
}

func (s *Session) writeTreeInto(tree *object.Tree, root *os.Root, rootDir, dir string, kept map[string]bool) error {
	for _, e := range tree.Entries {
		name, err := materializeTreeEntryName(e.Name)
		if err != nil {
			return err
		}
		full := filepath.Join(dir, name)
		rel, err := materializeRootRel(rootDir, full)
		if err != nil {
			return err
		}
		kept[full] = true
		switch e.Mode {
		case filemode.Dir:
			sub, err := object.GetTree(s.Sidecar.repo.Storer, e.Hash)
			if err != nil {
				return fmt.Errorf("materialize: subtree %s: %w", name, err)
			}
			if err := prepareMaterializeDir(root, rel); err != nil {
				return err
			}
			if err := s.writeTreeInto(sub, root, rootDir, full, kept); err != nil {
				return err
			}
		case filemode.Symlink:
			blob, err := s.readBlobString(e.Hash)
			if err != nil {
				return err
			}
			if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := root.Symlink(blob, rel); err != nil {
				return fmt.Errorf("materialize: symlink %s: %w", full, err)
			}
		case filemode.Executable, filemode.Regular:
			r, err := s.openBlobReaderLimited(e.Hash, maxMaterializedBlobBytes)
			if err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if e.Mode == filemode.Executable {
				perm = 0o755
			}
			if err := writeMaterializedFile(root, rel, r, perm, maxMaterializedBlobBytes); err != nil {
				_ = r.Close()
				return err
			}
			if err := r.Close(); err != nil {
				return err
			}
		}
	}
	kept[dir] = true // keep the dir itself when pruning
	return nil
}

func materializeRootRel(rootDir, path string) (string, error) {
	rel, err := filepath.Rel(rootDir, path)
	if err != nil {
		return "", err
	}
	if rel == "." || filepath.IsAbs(rel) ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("materialize: path %q escapes destination", path)
	}
	return rel, nil
}

func prepareMaterializeDir(root *os.Root, rel string) error {
	if info, err := root.Lstat(rel); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if err := root.Remove(rel); err != nil {
				return err
			}
		} else if !info.IsDir() {
			return fmt.Errorf("materialize: %q is not a directory", rel)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return workdirpath.MkdirAllRootNoSymlink(root, rel, 0o750)
}

func writeMaterializedFile(root *os.Root, rel string, r io.Reader, perm os.FileMode, maxBytes int64) error {
	if info, err := root.Lstat(rel); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if err := root.Remove(rel); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if maxBytes < 0 {
		_ = f.Close()
		_ = root.Remove(rel)
		return fmt.Errorf("materialize: negative file size limit")
	}
	if _, err := io.Copy(f, io.LimitReader(r, maxBytes)); err != nil {
		_ = f.Close()
		_ = root.Remove(rel)
		return err
	}
	var probe [1]byte
	n, err := io.ReadFull(r, probe[:])
	if n > 0 {
		_ = f.Close()
		_ = root.Remove(rel)
		return fmt.Errorf("materialize: blob exceeds %d bytes: %s", maxBytes, rel)
	}
	if err != nil && err != io.EOF {
		_ = f.Close()
		_ = root.Remove(rel)
		return err
	}
	return f.Close()
}

func materializeTreeEntryName(name string) (string, error) {
	if name == "" || name == "." || name == ".." ||
		!filepath.IsLocal(name) || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("materialize: invalid tree entry name %q", name)
	}
	return name, nil
}

func (s *Session) openBlobReaderLimited(hash plumbing.Hash, maxBytes int64) (io.ReadCloser, error) {
	blob, err := object.GetBlob(s.Sidecar.repo.Storer, hash)
	if err != nil {
		return nil, err
	}
	if blob.Size > maxBytes {
		return nil, fmt.Errorf("blob exceeds %d bytes: %s", maxBytes, hash)
	}
	return blob.Reader()
}

func (s *Session) readBlobLimited(hash plumbing.Hash, maxBytes int64) ([]byte, error) {
	blob, err := object.GetBlob(s.Sidecar.repo.Storer, hash)
	if err != nil {
		return nil, err
	}
	if blob.Size > maxBytes {
		return nil, fmt.Errorf("blob exceeds %d bytes: %s", maxBytes, hash)
	}
	r, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("blob exceeds %d bytes: %s", maxBytes, hash)
	}
	return data, nil
}

func (s *Session) readBlobString(hash plumbing.Hash) (string, error) {
	data, err := s.readBlobLimited(hash, maxMaterializedSymlinkTargetBytes)
	return string(data), err
}

// pruneExtras removes every entry under dir that wasn't written as part of
// the materialisation. Walks the worktree; a file/dir is kept iff its full
// path is in the `kept` set or is a descendant of a kept directory that was
// listed.
func pruneExtras(root *os.Root, dir string, kept map[string]bool) error {
	var toRemove []string
	err := fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		rel := filepath.FromSlash(path)
		full := filepath.Join(dir, rel)
		if kept[full] {
			return nil
		}
		// Skip stado-internal files we never want to touch on revert.
		base := filepath.Base(rel)
		if base == ".stado-pid" || base == ".stado" || base == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		toRemove = append(toRemove, rel)
		if d.IsDir() {
			return fs.SkipDir // RemoveAll handles contents below
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range toRemove {
		if err := root.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func wipeDir(dir string) error {
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return wipeRoot(root)
}

func wipeRoot(root *os.Root) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".stado-pid" || e.Name() == ".stado" || e.Name() == ".git" {
			continue
		}
		if err := root.RemoveAll(e.Name()); err != nil {
			return err
		}
	}
	return nil
}
