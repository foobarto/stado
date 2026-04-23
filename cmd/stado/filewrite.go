package main

import (
	"io"
	"os"
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
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	return copyAndCloseFile(out, in)
}
