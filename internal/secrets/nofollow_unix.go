//go:build linux

package secrets

import "syscall"

// secretOpenNoFollow makes readSecretFile's open refuse to follow a symlink at
// the final path component (O_NOFOLLOW → ELOOP), so a symlinked secret can't be
// followed to an arbitrary target whose mode happens to be 0600.
const secretOpenNoFollow = syscall.O_NOFOLLOW
