package runtime

import (
	"fmt"
	"io"
	"os"
)

func readRootRegularFileLimited(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("file is a symlink: %s", name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular: %s", name)
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
		return nil, fmt.Errorf("file is not regular: %s", name)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while opening: %s", name)
	}
	if openedInfo.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maxBytes, name)
	}
	return data, nil
}
