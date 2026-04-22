// Package instructions loads the project-root AGENTS.md (preferred)
// or CLAUDE.md (fallback) file and surfaces it as a system-prompt
// string. The convention is the same one Claude Code, Cursor, Aider,
// Opencode, and the `agents.md` proposal use: a repo-root markdown
// file describing project-specific guidance for an AI agent.
//
// Resolution: walk from cwd upward to the filesystem root. First file
// found wins. AGENTS.md is preferred over CLAUDE.md in the same
// directory — this matches the emerging cross-vendor convention
// (agents.md/ini) while still reading the widely-used CLAUDE.md
// fallback so existing repos light up without renaming.
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Names is the resolution order within a directory. First hit wins;
// later names are fallbacks for repos that predate the AGENTS.md
// convention. Adding new names is safe — they just become additional
// fallback candidates.
var Names = []string{"AGENTS.md", "CLAUDE.md"}

// Result reports what Load resolved. Content is empty iff no file was
// found; callers can safely pass Result.Content into their system
// prompt without a nil check.
type Result struct {
	Content string // file body; empty if no file was found
	Path    string // absolute path of the file; empty if not found
}

// Load walks from `start` upward and returns the first AGENTS.md /
// CLAUDE.md it finds. A clean miss (no file anywhere up the tree) is
// not an error — Result.Content is "" and Result.Path is "".
//
// Any I/O error (permissions, unreadable file) is returned verbatim;
// the caller decides whether to surface it as a warning or hard-fail.
// Stado's integration surfaces it as a stderr warning so a broken
// AGENTS.md doesn't brick the TUI.
func Load(start string) (Result, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return Result{}, fmt.Errorf("instructions: abs %s: %w", start, err)
	}
	// Walk: start, parent, parent-of-parent, ... stop at filesystem root.
	dir := abs
	for {
		for _, name := range Names {
			candidate := filepath.Join(dir, name)
			info, statErr := os.Lstat(candidate)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return Result{}, fmt.Errorf("instructions: lstat %s: %w", candidate, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				// Never auto-follow symlinks here: a repo-controlled AGENTS.md
				// symlink can otherwise exfiltrate arbitrary local files via the
				// system prompt path. Non-regular files are skipped for the same
				// reason.
				continue
			}
			body, readErr := os.ReadFile(candidate)
			if readErr != nil {
				return Result{}, fmt.Errorf("instructions: read %s: %w", candidate, readErr)
			}
			return Result{Content: string(body), Path: candidate}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Hit filesystem root without finding anything.
			return Result{}, nil
		}
		dir = parent
	}
}
