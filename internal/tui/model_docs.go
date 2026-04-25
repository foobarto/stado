package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/tui/filepicker"
)

func (m *Model) filePickerDocItems() []filepicker.Item {
	root := m.sidebarRepoRoot()
	if root == "" {
		root = m.cwd
	}
	if root == "" {
		return nil
	}
	paths := scanDocPaths(root)
	out := make([]filepicker.Item, 0, len(paths))
	for _, rel := range paths {
		out = append(out, filepicker.Item{
			Kind:    filepicker.KindDoc,
			ID:      rel,
			Display: rel,
			Meta:    "doc",
			Insert:  rel,
		})
	}
	return out
}

func scanDocPaths(root string) []string {
	const cap = 200
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return filepath.SkipDir
			}
			rel = filepath.ToSlash(rel)
			if skipDocDir(rel, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= cap {
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isDocPath(rel) {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func skipDocDir(rel, name string) bool {
	if strings.HasPrefix(name, ".") ||
		name == "node_modules" ||
		name == "vendor" ||
		name == "dist" ||
		name == "build" ||
		name == "target" {
		return true
	}
	if rel == "docs" || strings.HasPrefix(rel, "docs/") {
		return false
	}
	return true
}

func isDocPath(rel string) bool {
	lower := strings.ToLower(rel)
	if !(strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".mdx")) {
		return false
	}
	return !strings.Contains(rel, "/") || strings.HasPrefix(rel, "docs/")
}
