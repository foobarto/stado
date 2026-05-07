package workdirpath

import (
	"errors"
	"os"
	"path/filepath"
)

// Resolver provides workdir-confined path resolution and fs
// operations. The workdir is the trust boundary — paths resolved
// or operated on must end up under it after symlink resolution,
// or the operation fails closed.
//
// Phase 2.1 (A2) of the 2026-Q2 refactor program: this is the
// workdir-flavor entrypoint that absorbs the legacy package's 7
// workdir-anchored functions (`Resolve`, `RootRel`, `OpenReadFile`,
// `WriteFile`, `RootRelForWrite`, `Glob`, `GlobLimited`).
//
// One Resolver per workdir. Cheap to construct; safe to share
// across goroutines (immutable after New). Methods delegate to
// the legacy implementations during the migration window
// (2.1.a-c); legacy is rewritten as one-line wrappers on top of
// these methods at 2.1.d.
type Resolver struct {
	workdir string // canonical absolute path; populated by New
}

// New builds a Resolver anchored on workdir. The path is
// converted to absolute form once at construction so methods
// have a stable trust boundary regardless of the goroutine's
// current working directory.
//
// An empty workdir errors at construction. A non-existent
// workdir is accepted at construction — operations under it
// surface the missing-path error at call time, matching the
// legacy API's semantics.
func New(workdir string) (*Resolver, error) {
	if workdir == "" {
		return nil, errors.New("workdir required")
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, err
	}
	return &Resolver{workdir: abs}, nil
}

// Workdir returns the canonical absolute workdir the resolver
// was constructed with.
func (r *Resolver) Workdir() string { return r.workdir }

// Resolve canonicalises path against the workdir and confirms
// the result lies under the workdir after symlink resolution.
// A relative path is joined to the workdir; an absolute path
// is verified against it.
//
// Returns an error for paths that escape via symlink redirect.
// For paths that may not yet exist (create / write targets),
// use ResolveAllowMissing.
func (r *Resolver) Resolve(path string) (string, error) {
	return Resolve(r.workdir, path, false)
}

// ResolveAllowMissing is Resolve where the final path component
// (or trailing chain) may not exist on disk. The deepest
// existing ancestor is resolved through symlinks and the missing
// suffix is appended literally, suitable for create / write
// target paths.
func (r *Resolver) ResolveAllowMissing(path string) (string, error) {
	return Resolve(r.workdir, path, true)
}

// RootRel returns (canonical workdir, workdir-relative path) for
// the given path. The path must currently exist; for write
// targets that may not exist yet, use RootRelForWrite.
func (r *Resolver) RootRel(path string) (root, rel string, err error) {
	return RootRel(r.workdir, path, false)
}

// RootRelForWrite is RootRel for write targets — resolves the
// parent (allowing the final component to be missing) and
// returns the root-relative target ready for atomic-write.
func (r *Resolver) RootRelForWrite(path string) (root, rel string, err error) {
	return RootRelForWrite(r.workdir, path)
}

// OpenRegularFile opens path for reading via os.Root. Rejects
// symlinks (parent components AND final), non-regular files, and
// open-time swaps (SameFile check). Read-only by design — write
// callers use WriteFileAtomic.
//
// Returned *os.File is owned by the caller; close when done.
func (r *Resolver) OpenRegularFile(path string) (*os.File, error) {
	return OpenReadFile(r.workdir, path)
}

// WriteFileAtomic writes path through os.Root via tempfile +
// rename. Creates missing parent directories under the workdir
// (no-symlink walk). Rejects symlinked or non-regular existing
// targets — the rename-over semantics preserve the file's
// existing mode by default.
func (r *Resolver) WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return WriteFile(r.workdir, path, data, perm)
}

// Glob returns workdir-relative pattern matches. Bounded by the
// default glob walk limits; rejects absolute patterns and
// leading `..` escapes.
func (r *Resolver) Glob(pattern string) ([]string, error) {
	return Glob(r.workdir, pattern)
}

// GlobLimited is Glob with an explicit storage cap. Returns the
// matches slice (length capped at maxStored) plus the total
// count of all matches across the workdir.
func (r *Resolver) GlobLimited(pattern string, maxStored int) (matches []string, total int, err error) {
	return GlobLimited(r.workdir, pattern, maxStored)
}
