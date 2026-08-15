package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// RepositorySearchMatch is a bounded fixed-string match from an immutable
// session tree. Line is zero when the repository path itself matched.
type RepositorySearchMatch struct {
	Path string
	Line int
	Text string
}

// ReadFileAtHead reads one regular file from an immutable session tree-ref
// commit. It never follows worktree symlinks and refuses oversized blobs.
func (s *Session) ReadFileAtHead(head plumbing.Hash, name string, maxBytes int64) ([]byte, error) {
	name, err := normalizeRepositoryPath(name, false)
	if err != nil {
		return nil, err
	}
	tree, err := s.repositoryTreeAtHead(head)
	if err != nil {
		return nil, err
	}
	entry, err := tree.FindEntry(name)
	if err != nil {
		return nil, fmt.Errorf("repository read %s: %w", name, err)
	}
	if entry.Mode != filemode.Regular && entry.Mode != filemode.Executable {
		return nil, fmt.Errorf("repository read %s: only regular files are readable", name)
	}
	return s.readBlobLimited(entry.Hash, maxBytes)
}

// ListFilesAtHead returns at most maxFiles sorted tree paths plus whether more
// entries exist. The commit hash freezes the listing even if the worker moves.
func (s *Session) ListFilesAtHead(head plumbing.Hash, prefix string, maxFiles int) ([]string, bool, error) {
	if maxFiles < 1 {
		return nil, false, errors.New("repository list limit must be positive")
	}
	prefix, err := normalizeRepositoryPath(prefix, true)
	if err != nil {
		return nil, false, err
	}
	tree, err := s.repositoryTreeAtHead(head)
	if err != nil {
		return nil, false, err
	}
	files := make([]string, 0, maxFiles)
	more := false
	err = tree.Files().ForEach(func(file *object.File) error {
		if !repositoryPathWithin(file.Name, prefix) {
			return nil
		}
		if len(files) >= maxFiles {
			more = true
			return storer.ErrStop
		}
		files = append(files, file.Name)
		return nil
	})
	return files, more, err
}

// SearchFilesAtHead performs a case-insensitive fixed-string search over an
// immutable session tree. It returns at most maxMatches and explicitly reports
// when file-count or byte-scan ceilings made the result partial.
func (s *Session) SearchFilesAtHead(head plumbing.Hash, prefix, pattern string, maxMatches, maxFiles int, maxScanBytes int64) ([]RepositorySearchMatch, bool, error) {
	if maxMatches < 1 || maxFiles < 1 || maxScanBytes < 1 {
		return nil, false, errors.New("repository search limits must be positive")
	}
	prefix, err := normalizeRepositoryPath(prefix, true)
	if err != nil {
		return nil, false, err
	}
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, false, errors.New("repository search pattern required")
	}
	tree, err := s.repositoryTreeAtHead(head)
	if err != nil {
		return nil, false, err
	}
	matches := make([]RepositorySearchMatch, 0, maxMatches)
	var scanned int64
	visited, partial := 0, false
	err = tree.Files().ForEach(func(file *object.File) error {
		if !repositoryPathWithin(file.Name, prefix) {
			return nil
		}
		visited++
		if visited > maxFiles {
			partial = true
			return storer.ErrStop
		}
		if strings.Contains(strings.ToLower(file.Name), pattern) {
			matches = append(matches, RepositorySearchMatch{Path: file.Name})
			if len(matches) >= maxMatches {
				return storer.ErrStop
			}
		}
		if file.Size > maxScanBytes-scanned {
			partial = true
			return nil
		}
		reader, openErr := file.Reader()
		if openErr != nil {
			return openErr
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, file.Size+1))
		closeErr := reader.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		scanned += int64(len(data))
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			return nil
		}
		for lineNo, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(strings.ToLower(line), pattern) {
				continue
			}
			if utf8.RuneCountInString(line) > 2048 {
				line = string([]rune(line)[:2048])
			}
			matches = append(matches, RepositorySearchMatch{Path: file.Name, Line: lineNo + 1, Text: line})
			if len(matches) >= maxMatches {
				return storer.ErrStop
			}
		}
		return nil
	})
	return matches, partial, err
}

func (s *Session) repositoryTreeAtHead(head plumbing.Hash) (*object.Tree, error) {
	if head.IsZero() {
		return s.treeOrEmpty(plumbing.ZeroHash)
	}
	treeHash, err := s.TreeFromCommit(head)
	if err != nil {
		return nil, err
	}
	return s.treeOrEmpty(treeHash)
}

func normalizeRepositoryPath(name string, allowEmpty bool) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" && allowEmpty {
		return "", nil
	}
	clean := path.Clean(name)
	if clean == "." && allowEmpty {
		return "", nil
	}
	if name == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) {
		return "", errors.New("repository path must stay within the session tree")
	}
	return clean, nil
}

func repositoryPathWithin(name, prefix string) bool {
	return prefix == "" || name == prefix || strings.HasPrefix(name, prefix+"/")
}
