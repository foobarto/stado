//go:build windows

package secrets

// secretOpenNoFollow is a no-op on Windows: there is no O_NOFOLLOW and symlink
// semantics differ. readSecretFile's fstat-the-opened-fd + regular-file check
// still apply.
const secretOpenNoFollow = 0
