package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
)

const maxInstalledPluginEntries = 4096

// ListInstalledDirs returns installed plugin directory names under root.
// Directory entries are streamed in batches so a corrupted state directory
// cannot force one large in-memory listing.
func ListInstalledDirs(root string) ([]string, error) {
	return listInstalledDirs(root, maxInstalledPluginEntries)
}

func listInstalledDirs(root string, maxEntries int) ([]string, error) {
	pluginsRoot, err := workdirpath.OpenRootUnderUserConfig(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pluginsRoot.Close() }()
	dir, err := pluginsRoot.Open(".")
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
				return nil, fmt.Errorf("installed plugin directory contains more than %d entries", maxEntries)
			}
			name := e.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
				return nil, fmt.Errorf("invalid installed plugin entry name %q", name)
			}
			info, err := pluginsRoot.Lstat(name)
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
