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
	const limit = 200
	var out []string
	_ = walkTUIRepo(root, maxTUIRepoScanEntries, maxTUIRepoScanDepth, func(rel string, info os.FileInfo) tuiRepoWalkDecision {
		if info.IsDir() {
			if rel == "." {
				return tuiRepoWalkContinue
			}
			slashRel := filepath.ToSlash(rel)
			if skipDocDir(slashRel, filepath.Base(rel)) {
				return tuiRepoWalkSkipDir
			}
			return tuiRepoWalkContinue
		}
		if len(out) >= limit {
			return tuiRepoWalkStop
		}
		slashRel := filepath.ToSlash(rel)
		if isDocPath(slashRel) {
			out = append(out, slashRel)
		}
		return tuiRepoWalkContinue
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
