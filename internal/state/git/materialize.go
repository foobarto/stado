package git

import (
	"fmt"
	"io"
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
	maxMaterializedTreeEntries              = maxTreeEntries
	maxMaterializedTreeDepth                = maxTreeDepth
	maxMaterializedCleanupEntries           = maxTreeEntries
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
	state := &materializeState{maxEntries: maxMaterializedTreeEntries, maxDepth: maxMaterializedTreeDepth}
	if err := s.writeTreeInto(tree, root, dir, dir, kept, state, 0); err != nil {
		return err
	}

	if replacing {
		return pruneExtras(root, dir, kept)
	}
	return nil
}

type materializeState struct {
	entries    int
	maxEntries int
	maxDepth   int
}

func (s *Session) writeTreeInto(tree *object.Tree, root *os.Root, rootDir, dir string, kept map[string]bool, state *materializeState, depth int) error {
	if state == nil {
		state = &materializeState{maxEntries: maxMaterializedTreeEntries, maxDepth: maxMaterializedTreeDepth}
	}
	if depth > state.maxDepth {
		return fmt.Errorf("materialize: directory nesting exceeds %d: %s", state.maxDepth, dir)
	}
	for _, e := range tree.Entries {
		state.entries++
		if state.entries > state.maxEntries {
			return fmt.Errorf("materialize: tree contains more than %d entries", state.maxEntries)
		}
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
			if err := s.writeTreeInto(sub, root, rootDir, full, kept, state, depth+1); err != nil {
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
	state := &materializeCleanupState{maxEntries: maxMaterializedCleanupEntries, maxDepth: maxMaterializedTreeDepth}
	if err := collectPruneExtras(root, ".", dir, kept, state, 0); err != nil {
		return err
	}
	for _, p := range state.removals {
		if err := root.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

type materializeCleanupState struct {
	entries    int
	maxEntries int
	maxDepth   int
	removals   []string
}

func collectPruneExtras(root *os.Root, rel, rootDir string, kept map[string]bool, state *materializeCleanupState, depth int) error {
	if state == nil {
		state = &materializeCleanupState{maxEntries: maxMaterializedCleanupEntries, maxDepth: maxMaterializedTreeDepth}
	}
	if depth > state.maxDepth {
		return fmt.Errorf("materialize: cleanup nesting exceeds %d: %s", state.maxDepth, rel)
	}
	dir, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, e := range entries {
			state.entries++
			if state.entries > state.maxEntries {
				return fmt.Errorf("materialize: cleanup contains more than %d entries", state.maxEntries)
			}
			name := e.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return fmt.Errorf("materialize: invalid cleanup entry name %q", name)
			}
			childRel := name
			if rel != "." {
				childRel = filepath.Join(rel, name)
			}
			info, err := root.Lstat(childRel)
			if err != nil {
				return err
			}
			isDir := info.IsDir() && info.Mode()&os.ModeSymlink == 0
			full := filepath.Join(rootDir, childRel)
			if kept[full] {
				if isDir {
					if err := collectPruneExtras(root, childRel, rootDir, kept, state, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			// Skip stado-internal files we never want to touch on revert.
			base := filepath.Base(childRel)
			if base == ".stado-pid" || base == ".stado" || base == ".git" {
				continue
			}
			state.removals = append(state.removals, childRel)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
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
	toRemove, err := collectWipeRootEntries(root, maxMaterializedCleanupEntries)
	if err != nil {
		return err
	}
	for _, name := range toRemove {
		if err := root.RemoveAll(name); err != nil {
			return err
		}
	}
	return nil
}

func collectWipeRootEntries(root *os.Root, maxEntries int) ([]string, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	var toRemove []string
	entriesSeen := 0
	for {
		entries, readErr := dir.ReadDir(128)
		for _, e := range entries {
			entriesSeen++
			if entriesSeen > maxEntries {
				return nil, fmt.Errorf("materialize: wipe contains more than %d entries", maxEntries)
			}
			name := e.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("materialize: invalid wipe entry name %q", name)
			}
			if name == ".stado-pid" || name == ".stado" || name == ".git" {
				continue
			}
			toRemove = append(toRemove, name)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return toRemove, nil
}
