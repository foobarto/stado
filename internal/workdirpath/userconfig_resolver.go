package workdirpath

import "os"

// UserConfigResolver provides path-confined fs operations
// anchored on the operator's HOME / XDG directories. Used for
// stado's per-user state directories (config, data, state,
// cache) plus the audit-key directory and the per-user worktree
// root.
//
// Trust model: the longest HOME / XDG_*_HOME ancestor that
// covers the requested path is treated as the operator's
// environment — the system's symlink chain UP TO that anchor
// is accepted (so e.g. Fedora Atomic's `/home → /var/home`
// resolves correctly). The chain BELOW the anchor is walked
// with strict no-symlink enforcement, defending against
// in-user-space attackers planting redirects.
//
// When the requested path has no covering HOME / XDG anchor,
// the resolver falls back to strict no-symlink semantics from
// `/`. This matches the legacy `*UnderUserConfig` functions'
// fallback contract.
//
// Phase 2.1 (A2) of the 2026-Q2 refactor program: this is the
// user-config-flavor entrypoint that absorbs the legacy
// package's 5 user-config-anchored functions
// (`OpenRootUnderUserConfig`, `MkdirAllUnderUserConfig`,
// `ReadRegularFileUnderUserConfigLimited`,
// `ReadRegularFileUnderUserConfigNoLimit`,
// `OpenRegularFileUnderUserConfig`).
//
// The resolver reads the relevant environment variables
// (XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_STATE_HOME,
// XDG_CACHE_HOME, HOME) at each operation rather than
// caching at construction time. This matches the legacy
// `userTrustAnchor` behavior and avoids surprise when a
// long-running process inherits an updated environment
// (e.g. via `os.Setenv` from a test).
type UserConfigResolver struct{}

// NewUserConfigResolver builds a UserConfigResolver. No
// configuration is required — the environment-detected
// anchors are looked up per call.
func NewUserConfigResolver() *UserConfigResolver {
	return &UserConfigResolver{}
}

// OpenRoot opens path as an *os.Root with no-symlink
// enforcement below the anchor. The caller takes ownership of
// the returned root; close when done.
func (uc *UserConfigResolver) OpenRoot(path string) (*os.Root, error) {
	return openRootUnderUserConfig(path)
}

// OpenRegularFile opens path for reading, rejecting symlinked
// directory components below the anchor, symlinked final
// paths, and non-regular files. Includes a SameFile TOCTOU
// check between Lstat and Open. Read-only by design.
func (uc *UserConfigResolver) OpenRegularFile(path string) (*os.File, error) {
	return openRegularFileUnderUserConfig(path)
}

// ReadFileLimited reads at most maxBytes from path with the
// same trust model. Files larger than maxBytes return an error
// rather than silent truncation.
func (uc *UserConfigResolver) ReadFileLimited(path string, maxBytes int64) ([]byte, error) {
	return readRegularFileUnderUserConfigLimited(path, maxBytes)
}

// ReadFileNoLimit reads the entire file. Use only for paths
// where the caller has independent confidence the file is
// bounded — there's no size cap here.
func (uc *UserConfigResolver) ReadFileNoLimit(path string) ([]byte, error) {
	return readRegularFileUnderUserConfigNoLimit(path)
}

// MkdirAll creates path and any missing components. The chain
// up to the HOME / XDG anchor is created if needed (matching
// the legacy "anchor-as-operator-environment" rule); the chain
// below the anchor is walked with no-symlink enforcement.
func (uc *UserConfigResolver) MkdirAll(path string, perm os.FileMode) error {
	return mkdirAllUnderUserConfig(path, perm)
}

// RemoveAll removes path and its descendants with the same trust
// model as the other UserConfigResolver methods: chain UP TO
// the HOME / XDG anchor accepts system symlinks (so deletes
// under `/home/user/...` work on Atomic Fedora / Bazzite where
// `/home` is symlinked to `/var/home`); chain BELOW the anchor
// is walked no-symlink and a final symlink at the target is
// rejected (so a planted symlink can't redirect a delete to
// off-tree contents).
//
// Paths outside any HOME / XDG anchor fall back to strict
// no-symlink from `/` (matching `StrictResolver.RemoveAll` /
// the legacy `RemoveAllNoSymlink`). The fallback preserves the
// pre-EP-0028 semantics for non-HOME paths.
//
// Idempotent: removing a path that doesn't exist returns nil.
//
// EP-0028 round-2: the legacy `RemoveAllNoSymlink` was the
// strict-from-/ walk only. Atomic-Fedora hosts (Silverblue,
// Bazzite, Kinoite) have `/home → /var/home` as a system
// symlink, so any `RemoveAllNoSymlink(/home/user/.local/...)`
// call rejected at the `/home` component. EP-0028 added the
// `*UnderUserConfig` family for read/open/mkdir but
// `RemoveAllNoSymlink` was never given an Under-equivalent —
// this method closes that gap.
func (uc *UserConfigResolver) RemoveAll(path string) error {
	return removeAllUnderUserConfig(path)
}
