//go:build !airgap

package plugins

import (
	"fmt"
	"io"
)

const maxOnlinePluginResponseBytes int64 = 1 << 20

func readOnlinePluginBody(r io.Reader, label string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxOnlinePluginResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxOnlinePluginResponseBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxOnlinePluginResponseBytes)
	}
	return data, nil
}
