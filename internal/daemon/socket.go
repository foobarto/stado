package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// SocketPath returns the platform-default UDS path for the current uid.
// Resolution order:
//
//  1. $STADO_DAEMON_SOCKET if set (operator override).
//  2. $XDG_RUNTIME_DIR/stado/daemon.sock on Linux when XDG_RUNTIME_DIR
//     is set — that's a tmpfs created at login, owner-only by default,
//     and auto-cleaned at logout. Right home for ephemeral per-uid state.
//  3. Fallback: $TMPDIR/stado-<uid>/daemon.sock.
//
// Returns an error only if uid lookup fails on a platform where it's
// supposed to be available (i.e., never on Linux/macOS).
func SocketPath() (string, error) {
	if p := os.Getenv("STADO_DAEMON_SOCKET"); p != "" {
		return p, nil
	}
	uid := os.Getuid()
	if runtime.GOOS == "linux" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			return filepath.Join(rt, "stado", "daemon.sock"), nil
		}
	}
	tmp := os.TempDir()
	if uid < 0 {
		// On Windows os.Getuid returns -1 — caller should set
		// STADO_DAEMON_SOCKET explicitly. Returning a deterministic
		// fallback so unit tests on non-unix machines have something.
		return filepath.Join(tmp, "stado", "daemon.sock"), nil
	}
	return filepath.Join(tmp, "stado-"+strconv.Itoa(uid), "daemon.sock"), nil
}

// EnsureSocketDir creates the parent directory of socketPath at mode
// 0700 if it doesn't already exist. When the directory is freshly
// created it's chmod'd to 0700 to override any umask-widened mode;
// when it already exists we leave its mode alone (the operator may
// have intentionally chosen looser permissions on a custom path,
// and chmod'ing a directory we don't own — like /tmp — fails with
// EPERM).
//
// Idempotent: returns nil if the directory already exists. Errors only
// on real IO/permission failures during creation.
func EnsureSocketDir(socketPath string) error {
	dir := filepath.Dir(socketPath)
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("daemon: socket parent %s exists but is not a directory", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("daemon: stat socket dir %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: create socket dir %s: %w", dir, err)
	}
	// Tighten permissions on the directory we just created — umask
	// may have widened MkdirAll's mode arg. Only chmod what we owned-
	// from-creation; existing parent dirs we walked through stay
	// untouched.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: chmod socket dir %s: %w", dir, err)
	}
	return nil
}

// RemoveStaleSocket removes a socket file if no daemon is currently
// listening on it. "Currently listening" is decided by attempting a
// dial: success → live, refused/unreachable → stale.
//
// Returns:
//   - (true, nil) if a stale socket was removed.
//   - (false, nil) if no socket file existed.
//   - (false, err) if a daemon was found alive (the err is non-nil with
//     a "socket in use" message — callers can distinguish via errors.Is
//     against ErrSocketInUse).
//   - (false, err) for IO errors.
//
// The dial is short-deadlined (250 ms) so a sluggish daemon doesn't
// stall startup. If the daemon is alive but unresponsive longer than
// that, the operator gets a clear "socket in use" error rather than a
// silent overwrite that orphans the running daemon.
func RemoveStaleSocket(socketPath string) (bool, error) {
	info, err := os.Lstat(socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("daemon: stat socket: %w", err)
	}
	// Refuse to touch anything that isn't a socket — saves the operator
	// from a typo'd STADO_DAEMON_SOCKET pointing at a real file.
	if info.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("daemon: %s exists but is not a unix socket; refusing to remove", socketPath)
	}
	live, derr := pingSocket(socketPath)
	if live {
		return false, fmt.Errorf("%w: %s (run `stado daemon stop` first)", ErrSocketInUse, socketPath)
	}
	_ = derr
	if err := os.Remove(socketPath); err != nil {
		return false, fmt.Errorf("daemon: remove stale socket: %w", err)
	}
	return true, nil
}

// ErrSocketInUse is returned by RemoveStaleSocket when a live daemon
// owns the socket. Wrapped — callers use errors.Is to discriminate.
var ErrSocketInUse = errors.New("daemon: socket in use")
