package git

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
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
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("materialize: mkdir %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
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
		return pruneExtras(dir, kept)
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
			data, err := s.readBlob(e.Hash)
			if err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if e.Mode == filemode.Executable {
				perm = 0o755
			}
			if err := writeMaterializedFile(root, rel, data, perm); err != nil {
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
	return root.MkdirAll(rel, 0o750)
}

func writeMaterializedFile(root *os.Root, rel string, data []byte, perm os.FileMode) error {
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
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
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

func (s *Session) readBlob(hash plumbing.Hash) ([]byte, error) {
	blob, err := object.GetBlob(s.Sidecar.repo.Storer, hash)
	if err != nil {
		return nil, err
	}
	r, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (s *Session) readBlobString(hash plumbing.Hash) (string, error) {
	data, err := s.readBlob(hash)
	return string(data), err
}

// pruneExtras removes every entry under dir that wasn't written as part of
// the materialisation. Walks the worktree; a file/dir is kept iff its full
// path is in the `kept` set or is a descendant of a kept directory that was
// listed.
func pruneExtras(dir string, kept map[string]bool) error {
	var toRemove []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if kept[path] {
			return nil
		}
		// Skip stado-internal files we never want to touch on revert.
		base := filepath.Base(path)
		if base == ".stado-pid" || base == ".stado" || base == ".git" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		toRemove = append(toRemove, path)
		if info.IsDir() {
			return filepath.SkipDir // RemoveAll handles contents below
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range toRemove {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func wipeDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".stado-pid" || e.Name() == ".stado" || e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
