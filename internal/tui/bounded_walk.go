package tui

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
	maxTUIRepoScanEntries = 200000
	maxTUIRepoScanDepth   = 128
	tuiRepoScanReadBatch  = 128
)

type tuiRepoWalkDecision uint8

const (
	tuiRepoWalkContinue tuiRepoWalkDecision = iota
	tuiRepoWalkSkipDir
	tuiRepoWalkStop
)

var errTUIRepoWalkStop = errors.New("tui repo walk stopped")

type tuiRepoWalkState struct {
	entries    int
	maxEntries int
	maxDepth   int
}

func walkTUIRepo(rootPath string, maxEntries, maxDepth int, visit func(rel string, info os.FileInfo) tuiRepoWalkDecision) error {
	root, err := workdirpath.OpenRootNoSymlink(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	state := &tuiRepoWalkState{entries: 1, maxEntries: maxEntries, maxDepth: maxDepth}
	if state.entries > state.maxEntries {
		return fmt.Errorf("tui repo scan contains more than %d entries", state.maxEntries)
	}
	err = walkTUIRepoRel(root, ".", state, 0, visit)
	if errors.Is(err, errTUIRepoWalkStop) {
		return nil
	}
	return err
}

func walkTUIRepoRel(root *os.Root, rel string, state *tuiRepoWalkState, depth int, visit func(rel string, info os.FileInfo) tuiRepoWalkDecision) error {
	if depth > state.maxDepth {
		return fmt.Errorf("tui repo scan nesting exceeds %d: %s", state.maxDepth, filepath.ToSlash(rel))
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	decision := visit(rel, info)
	if decision == tuiRepoWalkStop {
		return errTUIRepoWalkStop
	}
	if !info.IsDir() {
		return nil
	}
	if decision == tuiRepoWalkSkipDir {
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
		return fmt.Errorf("tui repo scan directory changed while opening: %s", filepath.ToSlash(rel))
	}

	names, err := readTUIRepoDirNames(dir, state)
	if err != nil {
		return err
	}
	for _, name := range names {
		childRel := name
		if rel != "." {
			childRel = filepath.Join(rel, name)
		}
		if err := walkTUIRepoRel(root, childRel, state, depth+1, visit); err != nil {
			return err
		}
	}
	return nil
}

func readTUIRepoDirNames(dir *os.File, state *tuiRepoWalkState) ([]string, error) {
	var names []string
	for {
		entries, readErr := dir.ReadDir(tuiRepoScanReadBatch)
		for _, entry := range entries {
			state.entries++
			if state.entries > state.maxEntries {
				return nil, fmt.Errorf("tui repo scan contains more than %d entries", state.maxEntries)
			}
			name := entry.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("invalid tui repo scan entry name %q", name)
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
