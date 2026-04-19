package git

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("materialize: mkdir %s: %w", dir, err)
	}

	// Track which paths we wrote so we can prune stale files when replacing.
	kept := map[string]bool{}
	if err := s.writeTreeInto(tree, dir, kept); err != nil {
		return err
	}

	if replacing {
		return pruneExtras(dir, kept)
	}
	return nil
}

func (s *Session) writeTreeInto(tree *object.Tree, dir string, kept map[string]bool) error {
	for _, e := range tree.Entries {
		full := filepath.Join(dir, e.Name)
		kept[full] = true
		switch e.Mode {
		case filemode.Dir:
			sub, err := object.GetTree(s.Sidecar.repo.Storer, e.Hash)
			if err != nil {
				return fmt.Errorf("materialize: subtree %s: %w", e.Name, err)
			}
			if err := os.MkdirAll(full, 0o755); err != nil {
				return err
			}
			if err := s.writeTreeInto(sub, full, kept); err != nil {
				return err
			}
		case filemode.Symlink:
			blob, err := s.readBlobString(e.Hash)
			if err != nil {
				return err
			}
			_ = os.Remove(full)
			if err := os.Symlink(blob, full); err != nil {
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
			if err := os.WriteFile(full, data, perm); err != nil {
				return err
			}
		}
	}
	kept[dir] = true // keep the dir itself when pruning
	return nil
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
