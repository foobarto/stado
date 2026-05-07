package workdirpath

import (
	"errors"
	"fmt"
	"os"
)

// StrictResolver provides path-confined fs operations with
// strict no-symlink enforcement from the absolute filesystem
// root. Use it for genuinely untrusted writes — in-tmpdir test
// targets, in-repo paths supplied by an adversary, plugin
// sandbox writes — where the entire path chain must be
// validated.
//
// Phase 2.1 (A2) of the 2026-Q2 refactor program: this is the
// strict-no-symlink entrypoint that absorbs the legacy package's
// 5 strict-flavor functions (`OpenRootNoSymlink`,
// `OpenRegularFileNoSymlink`, `ReadRegularFileNoSymlinkLimited`,
// `MkdirAllNoSymlink`, `RemoveAllNoSymlink`) plus the 2
// ancestor-walk variants via the `Under(ancestor)` derivation
// (`OpenRootNoSymlinkUnder`, `MkdirAllNoSymlinkUnder`).
//
// Threat model: every directory component in the walked path
// must be a real directory, not a symlink. The first symlink
// rejects the operation. This is the strongest of the 4
// resolvers and the right choice when the path's chain is
// adversary-influenced.
//
// `Under(ancestor)` derivations relax the rule for the chain
// UP TO the ancestor (system symlinks like /home → /var/home
// accepted) but keep no-symlink below the anchor. The derived
// resolver's path arguments are interpreted RELATIVE TO the
// ancestor.
type StrictResolver struct {
	// ancestor, when non-empty, scopes the resolver to a trusted
	// ancestor: the path UP TO ancestor accepts system symlinks
	// (opened via os.OpenRoot), the path BELOW ancestor is
	// no-symlink. Path arguments to methods are
	// ancestor-relative.
	//
	// When empty (the default after NewStrictResolver), the
	// resolver walks no-symlink from the absolute root.
	ancestor string
}

// NewStrictResolver returns a StrictResolver that walks
// no-symlink from the filesystem root. Path arguments to its
// methods may be absolute or relative to the goroutine's
// current working directory.
func NewStrictResolver() *StrictResolver {
	return &StrictResolver{}
}

// Under returns a derived StrictResolver scoped to the given
// trusted ancestor. The resulting resolver's `OpenRoot` and
// `MkdirAll` methods accept paths RELATIVE TO ancestor and
// walk no-symlink below it; the chain UP TO ancestor itself is
// opened via os.OpenRoot, accepting whatever system symlinks
// the operating environment provides.
//
// `OpenRegularFile`, `ReadFileLimited`, and `RemoveAll` are
// NOT supported on a derived resolver — those operations don't
// have an ancestor-bound variant in the legacy API. Calls to
// them on an Under-derived resolver return a defined error
// rather than silently using the strict-from-/ path (which
// would be a behavior change on systems where the ancestor
// crosses a system symlink).
//
// ancestor must be a non-empty path; an empty ancestor errors.
func (s *StrictResolver) Under(ancestor string) (*StrictResolver, error) {
	if ancestor == "" {
		return nil, errors.New("ancestor required")
	}
	return &StrictResolver{ancestor: ancestor}, nil
}

// OpenRoot opens path as an *os.Root. On a strict-from-/
// resolver, path may be absolute or relative; the resolver
// walks no-symlink from the volume root. On an Under-derived
// resolver, path is relative to the ancestor and the chain
// above the ancestor accepts system symlinks.
//
// Caller takes ownership of the returned root; close when done.
func (s *StrictResolver) OpenRoot(path string) (*os.Root, error) {
	if s.ancestor == "" {
		return OpenRootNoSymlink(path)
	}
	return OpenRootNoSymlinkUnder(s.ancestor, path)
}

// MkdirAll creates path and any missing components. On a
// strict-from-/ resolver, path is absolute or cwd-relative
// and every component is no-symlink. On an Under-derived
// resolver, path is ancestor-relative; the chain above the
// ancestor is opened with system-symlinks accepted, the chain
// below is no-symlink + created if missing.
func (s *StrictResolver) MkdirAll(path string, perm os.FileMode) error {
	if s.ancestor == "" {
		return MkdirAllNoSymlink(path, perm)
	}
	return MkdirAllNoSymlinkUnder(s.ancestor, path, perm)
}

// OpenRegularFile opens path for reading via os.Root, rejecting
// symlinked directory components, symlinked final paths, and
// non-regular files. Includes a SameFile TOCTOU check.
//
// Returns an error on Under-derived resolvers — the legacy API
// has no ancestor-bound variant for this operation.
func (s *StrictResolver) OpenRegularFile(path string) (*os.File, error) {
	if s.ancestor != "" {
		return nil, fmt.Errorf("OpenRegularFile: not supported on ancestor-bound resolver (ancestor=%q)", s.ancestor)
	}
	return OpenRegularFileNoSymlink(path)
}

// ReadFileLimited reads at most maxBytes from path with strict
// no-symlink enforcement. Files larger than maxBytes return an
// error rather than silent truncation.
//
// Returns an error on Under-derived resolvers — see
// OpenRegularFile.
func (s *StrictResolver) ReadFileLimited(path string, maxBytes int64) ([]byte, error) {
	if s.ancestor != "" {
		return nil, fmt.Errorf("ReadFileLimited: not supported on ancestor-bound resolver (ancestor=%q)", s.ancestor)
	}
	return ReadRegularFileNoSymlinkLimited(path, maxBytes)
}

// RemoveAll removes path with strict no-symlink enforcement —
// symlinked directory components or a final symlink are
// rejected rather than silently followed and removed.
//
// Returns an error on Under-derived resolvers — see
// OpenRegularFile.
func (s *StrictResolver) RemoveAll(path string) error {
	if s.ancestor != "" {
		return fmt.Errorf("RemoveAll: not supported on ancestor-bound resolver (ancestor=%q)", s.ancestor)
	}
	return RemoveAllNoSymlink(path)
}
