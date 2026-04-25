package memory

import (
	"os"
	"path/filepath"
	"strings"
)

const sessionMemoryDisabledFile = "memory-disabled"

// SessionDisabled reports whether approved-memory retrieval is disabled
// for the current worktree/session. The marker lives under .stado so it
// stays local to the checked-out session and is ignored by git.
func SessionDisabled(workdir string) bool {
	_, err := os.Stat(sessionMemoryDisabledPath(workdir))
	return err == nil
}

// SetSessionDisabled toggles the current worktree/session marker used by
// PromptContext to skip approved-memory retrieval.
func SetSessionDisabled(workdir string, disabled bool) error {
	path := sessionMemoryDisabledPath(workdir)
	if disabled {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("disabled\n"), 0o600)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func sessionMemoryDisabledPath(workdir string) string {
	return filepath.Join(sessionControlRoot(workdir), ".stado", sessionMemoryDisabledFile)
}

func sessionControlRoot(workdir string) string {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		workdir = "."
	}
	dir, err := filepath.Abs(workdir)
	if err != nil {
		return workdir
	}
	original := dir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, ".stado", "user-repo")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return original
		}
		dir = parent
	}
}
