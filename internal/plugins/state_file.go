package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
)

// withPluginFileLock serializes a read-modify-write transaction across stado
// processes. Linux is the only supported platform (EP-0065), so flock is the
// deliberately small host primitive here. The lock itself is opened beneath
// the same no-symlink user-state resolver as the protected file.
func withPluginFileLock(path string, fn func() error) error {
	root, name, err := pluginStateRoot(path+".lock", true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var before os.FileInfo
	if info, statErr := root.Lstat(name); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("plugin state lock is not a regular file: %s", path+".lock")
		}
		before = info
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || (before != nil && !os.SameFile(before, opened)) {
		return fmt.Errorf("plugin state lock changed while opening: %s", path+".lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

const maxPluginStateFileBytes int64 = 16 << 20

func readPluginStateFile(path string) ([]byte, error) {
	root, name, err := pluginStateRoot(path, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("plugin state file is a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin state file is not a regular file: %s", path)
	}
	if info.Size() > maxPluginStateFileBytes {
		return nil, fmt.Errorf("plugin state file exceeds %d bytes: %s", maxPluginStateFileBytes, path)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin state file is not a regular file: %s", path)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("plugin state file changed while opening: %s", path)
	}
	if openedInfo.Size() > maxPluginStateFileBytes {
		return nil, fmt.Errorf("plugin state file exceeds %d bytes: %s", maxPluginStateFileBytes, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPluginStateFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxPluginStateFileBytes {
		return nil, fmt.Errorf("plugin state file exceeds %d bytes: %s", maxPluginStateFileBytes, path)
	}
	return data, nil
}

func writePluginStateFileAtomic(path string, data []byte, mode os.FileMode) error {
	if int64(len(data)) > maxPluginStateFileBytes {
		return fmt.Errorf("plugin state file exceeds %d bytes: %s", maxPluginStateFileBytes, path)
	}
	root, name, err := pluginStateRoot(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	tmpName := "." + name + "." + uuid.NewString() + ".tmp"
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmpName)
		}
	}()
	n, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if n != len(data) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

func pluginStateRoot(path string, createDir bool) (*os.Root, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", fmt.Errorf("plugin state path is empty")
	}
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == ".." || name == string(filepath.Separator) || strings.Contains(name, "\x00") {
		return nil, "", fmt.Errorf("invalid plugin state path: %s", path)
	}
	uc := workdirpath.NewUserConfigResolver()
	if createDir {
		// The no-symlink mkdir walker can lose a benign create race between
		// processes after both observe a missing final component. EEXIST only
		// means another contender created something at that name; OpenRoot below
		// still validates that it is a real directory beneath the trusted anchor.
		if err := uc.MkdirAll(dir, 0o700); err != nil && !os.IsExist(err) {
			return nil, "", err
		}
	}
	root, err := uc.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}
