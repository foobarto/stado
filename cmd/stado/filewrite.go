package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
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
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) // #nosec G304 -- callers validate destination scope and pass the required mode.
	if err != nil {
		return err
	}
	return copyAndCloseFile(out, in)
}

func writeRegularFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == ".." || strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid output path %q", path)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return workdirpath.WriteRootFileAtomic(root, name, data, mode)
}
