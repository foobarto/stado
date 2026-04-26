package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
)

type syncedWriteCloser interface {
	io.Writer
	Sync() error
	Close() error
}

func copyAndCloseFile(out syncedWriteCloser, in io.Reader) error {
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeReaderToPath(dst string, mode os.FileMode, in io.Reader) error {
	dir := filepath.Dir(dst)
	name := filepath.Base(dst)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid output path %q", dst)
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return writeRootReaderFileAtomic(root, name, mode, in)
}

func writeRootReaderFileAtomic(root *os.Root, name string, mode os.FileMode, in io.Reader) error {
	if root == nil {
		return fmt.Errorf("root unavailable")
	}
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file is not regular: %s", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dir, base := filepath.Split(name)
	tmp := filepath.Join(dir, "."+base+"."+uuid.NewString()+".tmp")
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = root.Remove(tmp)
		}
	}()
	if err := copyAndCloseFile(out, in); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	renamed = true
	return nil
}

func writeRegularFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid output path %q", path)
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return workdirpath.WriteRootFileAtomic(root, name, data, mode)
}
