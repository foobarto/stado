package lspfind

import (
	"fmt"
	"io"

	"github.com/foobarto/stado/internal/workdirpath"
)

const maxLSPDocumentBytes int64 = 4 << 20

func readLSPDocumentText(workdir, path string) (string, error) {
	r, err := workdirpath.New(workdir)
	if err != nil {
		return "", err
	}
	f, err := r.OpenRegularFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxLSPDocumentBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxLSPDocumentBytes {
		return "", fmt.Errorf("LSP document exceeds %d bytes: %s", maxLSPDocumentBytes, path)
	}
	return string(data), nil
}
