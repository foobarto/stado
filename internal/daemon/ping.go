package daemon

import (
	"context"
	"net"
	"time"
)

// pingSocket dials socketPath with a short deadline; success means a
// process is currently accepting connections. Returns (true, nil) for
// live, (false, err) otherwise (the err carries the dial failure for
// debugging; callers usually treat it as "stale" without inspecting).
//
// Splitting this into its own file keeps the platform-portable Lstat
// logic in socket.go free of the net package import — and makes it
// easy to swap the dial implementation in tests via build tags if we
// ever need to (currently we don't).
func pingSocket(socketPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return false, err
	}
	_ = conn.Close()
	return true, nil
}
