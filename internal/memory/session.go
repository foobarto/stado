package memory

import (
	"os"
	"path/filepath"
	"strings"
)

const sessionMemoryDisabledFile = "memory-disabled"
const userRepoPinFile = ".stado/user-repo"

// SessionDisabled reports whether approved-memory retrieval is disabled
// for the current worktree/session. The marker lives under .stado so it
// stays local to the checked-out session and is ignored by git.
func SessionDisabled(workdir string) bool {
	root, err := os.OpenRoot(sessionControlRoot(workdir))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	_, err = root.Stat(filepath.Join(".stado", sessionMemoryDisabledFile))
	return err == nil
}

// SetSessionDisabled toggles the current worktree/session marker used by
// PromptContext to skip approved-memory retrieval.
func SetSessionDisabled(workdir string, disabled bool) error {
	root, err := os.OpenRoot(sessionControlRoot(workdir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join(".stado", sessionMemoryDisabledFile)
	if disabled {
		if err := root.MkdirAll(".stado", 0o700); err != nil {
			return err
		}
		return root.WriteFile(path, []byte("disabled\n"), 0o600)
	}
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
		if hasUserRepoPin(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return original
		}
		dir = parent
	}
}

func hasUserRepoPin(workdir string) bool {
	root, err := os.OpenRoot(workdir)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	_, err = root.ReadFile(userRepoPinFile)
	return err == nil
}
