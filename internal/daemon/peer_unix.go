//go:build linux

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeerUID reads SO_PEERCRED from a unix-socket connection and
// rejects connections whose peer uid differs from the daemon's. The
// socket file mode (0700) already restricts to-owner-only at the
// filesystem layer; SO_PEERCRED is a defence-in-depth check that costs
// nothing and protects against a misconfigured umask or someone running
// `chmod 0777` on the parent directory.
//
// Returns nil on uid match (or when the connection isn't a unix socket;
// that case shouldn't happen in production but is harmless to allow).
// Linux-only build tag — other platforms get the no-op stub in
// peer_other.go.
func checkPeerUID(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		// Non-unix connection (e.g., test harness using net.Pipe).
		// SO_PEERCRED isn't applicable; let it through.
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer cred: syscall conn: %w", err)
	}
	var ucred *unix.Ucred
	var sockErr error
	cerr := raw.Control(func(fd uintptr) {
		ucred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if cerr != nil {
		return fmt.Errorf("peer cred: control: %w", cerr)
	}
	if sockErr != nil {
		return fmt.Errorf("peer cred: getsockopt: %w", sockErr)
	}
	if ucred == nil {
		return errors.New("peer cred: no ucred")
	}
	myUID := uint32(os.Getuid())
	if ucred.Uid != myUID {
		return fmt.Errorf("peer cred: uid %d != daemon uid %d", ucred.Uid, myUID)
	}
	return nil
}
