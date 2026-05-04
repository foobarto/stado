package git

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/foobarto/stado/internal/workdirpath"
)

const maxWorktreeSessionEntries = 200000

// ListWorktreeSessionIDs returns valid session IDs represented by real
// directories under worktreeRoot. Directory entries are streamed in batches to
// avoid materializing large state directories.
func ListWorktreeSessionIDs(worktreeRoot string) ([]string, error) {
	return listWorktreeSessionIDsLimited(worktreeRoot, maxWorktreeSessionEntries)
}

func listWorktreeSessionIDsLimited(worktreeRoot string, maxEntries int) ([]string, error) {
	root, err := workdirpath.OpenRootUnderUserConfig(worktreeRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()

	var ids []string
	entriesSeen := 0
	for {
		entries, readErr := dir.ReadDir(128)
		for _, e := range entries {
			entriesSeen++
			if entriesSeen > maxEntries {
				return nil, fmt.Errorf("worktree directory contains more than %d entries", maxEntries)
			}
			name := e.Name()
			if ValidateSessionID(name) != nil {
				continue
			}
			info, err := root.Lstat(name)
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue
			}
			ids = append(ids, name)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Strings(ids)
	return ids, nil
}
