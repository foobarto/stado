package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
)

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
	if createDir {
		if err := workdirpath.MkdirAllUnderUserConfig(dir, 0o700); err != nil {
			return nil, "", err
		}
	}
	root, err := workdirpath.OpenRootUnderUserConfig(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}
