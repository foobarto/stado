package git

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const maxTreeBlobBytes int64 = 256 << 20

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

		info, err := os.Lstat(full)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("build tree: stat %s: %w", full, err)
		}
		if info.IsDir() {
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
	if isSymlink {
		target, err := os.Readlink(path)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("readlink %s: %w", path, err)
		}
		return s.writeBlobReader(path, strings.NewReader(target), int64(len(target)))
	}

	f, err := workdirpath.OpenRegularFileNoSymlink(path)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if info.Size() > maxTreeBlobBytes {
		return plumbing.ZeroHash, fmt.Errorf("build tree: file exceeds %d bytes: %s", maxTreeBlobBytes, path)
	}
	return s.writeRegularBlob(path, f, info.Size())
}

func (s *Session) writeBlobReader(path string, r io.Reader, size int64) (plumbing.Hash, error) {
	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(size)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	n, err := io.Copy(w, r)
	if err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if n != size {
		_ = w.Close()
		return plumbing.ZeroHash, fmt.Errorf("build tree: file changed while reading: %s", path)
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.Sidecar.repo.Storer.SetEncodedObject(obj)
}

func (s *Session) writeRegularBlob(path string, f *os.File, size int64) (plumbing.Hash, error) {
	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(size)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	n, err := io.Copy(w, io.LimitReader(f, size))
	if err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if n != size {
		_ = w.Close()
		return plumbing.ZeroHash, fmt.Errorf("build tree: file changed while reading: %s", path)
	}
	var probe [1]byte
	extra, err := io.ReadFull(f, probe[:])
	if extra > 0 {
		_ = w.Close()
		return plumbing.ZeroHash, fmt.Errorf("build tree: file changed while reading: %s", path)
	}
	if err != nil && err != io.EOF {
		_ = w.Close()
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

// ChangedFilesBetween returns a sorted, deduplicated list of file paths that
// differ between two tree hashes. Zero hashes are treated as empty trees.
func (s *Session) ChangedFilesBetween(fromHash, toHash plumbing.Hash) ([]string, error) {
	if fromHash == toHash {
		return nil, nil
	}
	fromTree, err := s.treeOrEmpty(fromHash)
	if err != nil {
		return nil, err
	}
	toTree, err := s.treeOrEmpty(toHash)
	if err != nil {
		return nil, err
	}
	changes, err := fromTree.Diff(toTree)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if change.From.Name != "" {
			seen[change.From.Name] = struct{}{}
		}
		if change.To.Name != "" {
			seen[change.To.Name] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func (s *Session) treeOrEmpty(hash plumbing.Hash) (*object.Tree, error) {
	if hash.IsZero() {
		var err error
		hash, err = s.writeEmptyTree()
		if err != nil {
			return nil, err
		}
	}
	return object.GetTree(s.Sidecar.repo.Storer, hash)
}
