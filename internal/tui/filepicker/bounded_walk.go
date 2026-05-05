package filepicker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	maxRepoFileScanEntries = 200000
	maxRepoFileScanDepth   = 128
	repoFileScanReadBatch  = 128
)

type repoFileWalkDecision uint8

const (
	repoFileWalkContinue repoFileWalkDecision = iota
	repoFileWalkSkipDir
	repoFileWalkStop
)

var errRepoFileWalkStop = errors.New("repo file walk stopped")

type repoFileWalkState struct {
	entries    int
	maxEntries int
	maxDepth   int
}

func walkRepoFiles(rootPath string, maxEntries, maxDepth int, visit func(rel string, info os.FileInfo) repoFileWalkDecision) error {
	root, err := workdirpath.OpenRootUnderUserConfig(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	state := &repoFileWalkState{entries: 1, maxEntries: maxEntries, maxDepth: maxDepth}
	if state.entries > state.maxEntries {
		return fmt.Errorf("repo file scan contains more than %d entries", state.maxEntries)
	}
	err = walkRepoFileRel(root, ".", state, 0, visit)
	if errors.Is(err, errRepoFileWalkStop) {
		return nil
	}
	return err
}

func walkRepoFileRel(root *os.Root, rel string, state *repoFileWalkState, depth int, visit func(rel string, info os.FileInfo) repoFileWalkDecision) error {
	if depth > state.maxDepth {
		return fmt.Errorf("repo file scan nesting exceeds %d: %s", state.maxDepth, filepath.ToSlash(rel))
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	decision := visit(rel, info)
	if decision == repoFileWalkStop {
		return errRepoFileWalkStop
	}
	if !info.IsDir() {
		return nil
	}
	if decision == repoFileWalkSkipDir {
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
	info, err = root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !openedInfo.IsDir() {
		return nil
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("repo file scan directory changed while opening: %s", filepath.ToSlash(rel))
	}

	names, err := readRepoFileDirNames(dir, state)
	if err != nil {
		return err
	}
	for _, name := range names {
		childRel := name
		if rel != "." {
			childRel = filepath.Join(rel, name)
		}
		if err := walkRepoFileRel(root, childRel, state, depth+1, visit); err != nil {
			return err
		}
	}
	return nil
}

func readRepoFileDirNames(dir *os.File, state *repoFileWalkState) ([]string, error) {
	var names []string
	for {
		entries, readErr := dir.ReadDir(repoFileScanReadBatch)
		for _, entry := range entries {
			state.entries++
			if state.entries > state.maxEntries {
				return nil, fmt.Errorf("repo file scan contains more than %d entries", state.maxEntries)
			}
			name := entry.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("invalid repo file scan entry name %q", name)
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
