package memory

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
)

const (
	sessionMemoryDisabledFile = "memory-disabled"
	userRepoPinFile           = ".stado/user-repo"
	maxUserRepoPinFileBytes   = 64 << 10
)

// SessionDisabled reports whether approved-memory retrieval is disabled
// for the current worktree/session. The marker lives under .stado so it
// stays local to the checked-out session and is ignored by git.
func SessionDisabled(workdir string) bool {
	root, err := workdirpath.OpenRootUnderUserConfig(sessionControlRoot(workdir))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(filepath.Join(".stado", sessionMemoryDisabledFile))
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

// SetSessionDisabled toggles the current worktree/session marker used by
// PromptContext to skip approved-memory retrieval.
func SetSessionDisabled(workdir string, disabled bool) error {
	root, err := workdirpath.OpenRootUnderUserConfig(sessionControlRoot(workdir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join(".stado", sessionMemoryDisabledFile)
	if disabled {
		if err := workdirpath.MkdirAllRootNoSymlink(root, ".stado", 0o700); err != nil {
			return err
		}
		return writeSessionControlFile(root, path, []byte("disabled\n"), 0o600)
	}
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeSessionControlFile(root *os.Root, name string, data []byte, perm os.FileMode) error {
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("session control file is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("session control file is not regular: %s", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(name)
	base := filepath.Base(name)
	tmp := "." + base + "." + uuid.NewString() + ".tmp"
	if dir != "." {
		tmp = filepath.Join(dir, tmp)
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmp)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	keepTmp = true
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
		if workdirpath.LooksLikeRepoRoot(dir) {
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
	root, err := workdirpath.OpenRootUnderUserConfig(workdir)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	_, err = workdirpath.ReadRootRegularFileLimited(root, userRepoPinFile, maxUserRepoPinFileBytes)
	return err == nil
}
