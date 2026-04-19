//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ApplyLandlock narrows the current process's filesystem access via Linux's
// landlock LSM (PLAN §3.2). Pure-Go, no CGO — direct syscall invocation.
//
// Behaviour:
//   - Kernel < 5.13 or landlock disabled: returns ErrLandlockUnavailable
//     without modifying the process. Caller decides whether to fail open
//     or hard (stado fails open, emits a warning, continues).
//   - FSRead paths: LANDLOCK_ACCESS_FS_READ_FILE | READ_DIR for each.
//   - FSWrite paths: WRITE_FILE + MAKE_REG/DIR/SYM + REMOVE_FILE/DIR +
//     TRUNCATE. Covers what a coding agent realistically needs (write,
//     create, rename-within, delete).
//
// Landlock is irreversible: once restricted, this process cannot regain
// access. Apply at the outer boundary of a sandboxed subprocess.
func ApplyLandlock(p Policy) error {
	if len(p.FSRead) == 0 && len(p.FSWrite) == 0 {
		return nil // no FS restrictions requested
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("landlock: prctl no_new_privs: %w", err)
	}

	readMask := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	writeMask := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE |
		unix.LANDLOCK_ACCESS_FS_REFER)

	handled := readMask | writeMask

	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	fd, err := landlockCreateRuleset(&attr, unsafe.Sizeof(attr), 0)
	if err != nil {
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EOPNOTSUPP) {
			return ErrLandlockUnavailable
		}
		// EINVAL usually means our attr size or access-fs bits include flags
		// not supported by this kernel. Downgrade and try again.
		if errors.Is(err, syscall.EINVAL) {
			attr.Access_fs &^= unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_REFER
			fd, err = landlockCreateRuleset(&attr, unsafe.Sizeof(attr), 0)
			if err != nil {
				return fmt.Errorf("landlock: create ruleset: %w", err)
			}
			handled = attr.Access_fs
			writeMask &= handled
		} else {
			return fmt.Errorf("landlock: create ruleset: %w", err)
		}
	}
	defer unix.Close(fd)

	for _, path := range p.FSRead {
		if err := addPathBeneathRule(fd, path, readMask&handled); err != nil {
			return err
		}
	}
	for _, path := range p.FSWrite {
		// Read+write on write paths — otherwise the agent can't re-read a
		// file it just wrote.
		if err := addPathBeneathRule(fd, path, (readMask|writeMask)&handled); err != nil {
			return err
		}
	}

	if err := landlockRestrictSelf(fd, 0); err != nil {
		return fmt.Errorf("landlock: restrict self: %w", err)
	}
	return nil
}

// ErrLandlockUnavailable is returned when the kernel doesn't support
// landlock (either too old or disabled in the kernel config). Callers
// typically log it and continue unsandboxed.
var ErrLandlockUnavailable = errors.New("landlock: unsupported on this kernel (need ≥ 5.13)")

// addPathBeneathRule registers a LANDLOCK_RULE_PATH_BENEATH rule: "under
// `path`, these access bits are allowed." Uses O_PATH so we don't actually
// need read permission on the directory to take a reference.
func addPathBeneathRule(rulesetFD int, path string, access uint64) error {
	parentFD, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		// Non-existent paths aren't fatal — log and skip so stale policy
		// entries don't break startup.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("landlock: open %s: %w", path, err)
	}
	defer unix.Close(parentFD)

	rule := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(parentFD),
	}
	if err := landlockAddRule(rulesetFD, rulePathBeneath, unsafe.Pointer(&rule), 0); err != nil {
		return fmt.Errorf("landlock: add rule for %s: %w", path, err)
	}
	return nil
}

// --- raw syscall wrappers ---

const rulePathBeneath = 1 // LANDLOCK_RULE_PATH_BENEATH

func landlockCreateRuleset(attr *unix.LandlockRulesetAttr, size uintptr, flags uint32) (int, error) {
	fd, _, errno := syscall.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(attr)),
		size,
		uintptr(flags),
	)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func landlockAddRule(rulesetFD int, ruleType uint32, ruleAttr unsafe.Pointer, flags uint32) error {
	_, _, errno := syscall.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		uintptr(ruleType),
		uintptr(ruleAttr),
		uintptr(flags),
		0, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(rulesetFD int, flags uint32) error {
	_, _, errno := syscall.Syscall(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(rulesetFD),
		uintptr(flags),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
