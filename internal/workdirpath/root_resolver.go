package workdirpath

import "os"

// RootResolver provides path-confined fs operations against a
// caller-owned *os.Root handle. Use it when an *os.Root has
// already been opened (e.g. via UserConfigResolver.OpenRoot,
// StrictResolver.OpenRoot, or os.OpenRoot directly) and the
// caller wants to perform several operations against it without
// reopening the root on each call.
//
// Phase 2.1 (A2) of the 2026-Q2 refactor program: this is the
// *os.Root entrypoint that absorbs the legacy package's 4
// `*os.Root`-relative functions (`ReadRootRegularFileLimited`,
// `WriteRootFileAtomic`, `WriteRootFileAtomicExactMode`,
// `MkdirAllRootNoSymlink`).
//
// Independent of Resolver — there's no requirement that a
// workdir-anchored Resolver exist before constructing one.
// Round-A2 review (codex + gemini) explicitly rejected the
// `r.AtRoot(*os.Root)` derivation pattern as fake-resolver-state.
//
// Ownership: RootResolver BORROWS the *os.Root. The caller is
// responsible for closing the underlying *os.Root when done;
// RootResolver never closes it.
type RootResolver struct {
	root *os.Root
}

// NewRootResolver wraps an existing *os.Root for use with the
// resolver's atomic-write / mkdir / read methods. The caller
// retains ownership of root; close it when done.
//
// A nil root is accepted at construction; methods on the result
// surface a typed "root unavailable" error rather than nil-
// derefing, matching the legacy `*os.Root`-bearing functions'
// nil-tolerant behavior.
func NewRootResolver(root *os.Root) *RootResolver {
	return &RootResolver{root: root}
}

// Root returns the underlying *os.Root the resolver was
// constructed with. Useful when the caller needs to pass the
// raw handle to a different API; ownership stays with the
// caller.
func (rr *RootResolver) Root() *os.Root { return rr.root }

// ReadFileLimited opens name relative to the resolver's
// *os.Root and reads at most maxBytes. Rejects symlinks,
// non-regular files, and open-time swaps (SameFile check).
// Files larger than maxBytes return an error rather than
// silent truncation.
func (rr *RootResolver) ReadFileLimited(name string, maxBytes int64) ([]byte, error) {
	return ReadRootRegularFileLimited(rr.root, name, maxBytes)
}

// WriteFileAtomic writes data to name via tempfile + rename.
// Existing files have their mode preserved; new files are
// created with perm. Rejects symlinks and non-regular targets.
func (rr *RootResolver) WriteFileAtomic(name string, data []byte, perm os.FileMode) error {
	return WriteRootFileAtomic(rr.root, name, data, perm)
}

// WriteFileAtomicExactMode is WriteFileAtomic that always
// applies perm to the replacement file (no preservation of an
// existing file's mode). Use when the caller intends to
// overwrite the mode along with the contents.
func (rr *RootResolver) WriteFileAtomicExactMode(name string, data []byte, perm os.FileMode) error {
	return WriteRootFileAtomicExactMode(rr.root, name, data, perm)
}

// MkdirAll creates path and any missing parents relative to the
// resolver's *os.Root. Rejects symlinked components.
func (rr *RootResolver) MkdirAll(path string, perm os.FileMode) error {
	return MkdirAllRootNoSymlink(rr.root, path, perm)
}
