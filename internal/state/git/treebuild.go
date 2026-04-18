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

// BuildTreeFromDir recursively walks dir and writes blob + tree objects into
// the sidecar, returning the root tree hash. Hidden files starting with "." in
// the top level ARE included (matches `git add`). Caller's responsibility to
// pass an already-scoped directory (we don't apply .gitignore here).
func (s *Session) BuildTreeFromDir(dir string) (plumbing.Hash, error) {
	return s.buildTree(dir)
}

func (s *Session) buildTree(dir string) (plumbing.Hash, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("build tree: read %s: %w", dir, err)
	}
	var te []treeEntry
	for _, d := range entries {
		name := d.Name()
		// Skip common junk and VCS dirs that would bloat the tree ref.
		if name == ".git" || name == ".stado" {
			continue
		}
		full := filepath.Join(dir, name)

		if d.IsDir() {
			sub, err := s.buildTree(full)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			if sub.IsZero() {
				continue
			}
			te = append(te, treeEntry{name: name, hash: sub, mode: filemode.Dir})
			continue
		}

		info, err := d.Info()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("build tree: stat %s: %w", full, err)
		}
		mode := filemode.Regular
		if info.Mode()&os.ModeSymlink != 0 {
			mode = filemode.Symlink
		} else if info.Mode()&0o111 != 0 {
			mode = filemode.Executable
		}

		blobHash, err := s.writeBlob(full, mode == filemode.Symlink)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		te = append(te, treeEntry{name: name, hash: blobHash, mode: mode})
	}
	if len(te) == 0 {
		// Empty directory; represent as no entry (git has no empty trees at
		// non-root positions).
		return plumbing.ZeroHash, nil
	}
	return s.entriesToTree(te)
}

// writeBlob stores the contents of path (or symlink target) as a blob object
// and returns its hash.
func (s *Session) writeBlob(path string, isSymlink bool) (plumbing.Hash, error) {
	var r io.Reader
	var size int64

	if isSymlink {
		target, err := os.Readlink(path)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("readlink %s: %w", path, err)
		}
		r = strings.NewReader(target)
		size = int64(len(target))
	} else {
		f, err := os.Open(path)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		r = f
		size = info.Size()
	}

	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(size)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := io.Copy(w, r); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.Sidecar.repo.Storer.SetEncodedObject(obj)
}

// TreeFromCommit returns the tree hash of a commit.
func (s *Session) TreeFromCommit(hash plumbing.Hash) (plumbing.Hash, error) {
	c, err := object.GetCommit(s.Sidecar.repo.Storer, hash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return c.TreeHash, nil
}

// CurrentTree returns the tree hash of the current tree-ref head, or zero if
// the tree ref isn't set yet.
func (s *Session) CurrentTree() (plumbing.Hash, error) {
	head, err := s.TreeHead()
	if err != nil || head.IsZero() {
		return plumbing.ZeroHash, err
	}
	return s.TreeFromCommit(head)
}
