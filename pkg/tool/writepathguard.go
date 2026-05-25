package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrGitMetadataWrite is the canonical error type returned by
// [DefaultGitWritePathGuard] when a write targets a `.git` metadata
// path. Callers can `errors.Is` against it to distinguish guard
// rejections from other I/O errors. Hosts may wrap with their own
// error type ("acp host: ..." prefix etc.) before returning to the
// fs.write tool — wrap with %w so the sentinel survives the
// wrapping.
var ErrGitMetadataWrite = errors.New("refusing fs write into .git metadata path")

// DefaultGitWritePathGuard is the canonical implementation of the
// `.git`-write refusal that satisfies [WritePathGuard]'s contract.
// Four sibling hosts (acpwrap, acp.acpHost, daemonToolHost,
// subagent.ScopedWriteHost) previously inlined slight variations of
// this same check; PR #050 + Codex K (PR #63) + Codex 2026-05-25
// deep-dive's 3-way-convergent symlink + case-insensitive findings
// motivated factoring into one helper so the defense lands
// uniformly.
//
// Resolution model:
//
//  1. Relative `path` is joined against `workdir`; absolute `path`
//     is used as-is. `workdir` may be empty when the host has no
//     anchor (relative paths then resolve against the process cwd).
//  2. `filepath.EvalSymlinks` runs against the resolved path, walking
//     symlinks. When the target doesn't exist yet (typical for fs.write
//     create-new flows), EvalSymlinks fails — we fall back to
//     EvalSymlinks on the longest existing prefix, then re-append the
//     trailing non-existent segments lexically. This catches the
//     attack where `foo` is a symlink to `.git` and the operator
//     calls `fs.write foo/config` (would create `.git/config`).
//  3. Final path is split on `/` and segments are compared with
//     `strings.EqualFold` against `.git` so `.GIT/config` and
//     `.Git/HEAD` are rejected on macOS / Windows case-insensitive
//     filesystems where they address the same directory as `.git`.
//
// Hosts implementing CheckWritePath should call this helper +
// optionally wrap the error with their own provenance prefix:
//
//	func (h *acpHost) CheckWritePath(path string) error {
//	    if err := tool.DefaultGitWritePathGuard(h.workdir, path); err != nil {
//	        return fmt.Errorf("acp host: %w (%q)", err, path)
//	    }
//	    return nil
//	}
//
// Returns nil for paths that don't address a `.git` segment after
// the symlink-resolved + case-insensitive check.
func DefaultGitWritePathGuard(workdir, path string) error {
	resolved := path
	if !filepath.IsAbs(resolved) && workdir != "" {
		resolved = filepath.Join(workdir, resolved)
	}
	resolved = filepath.Clean(resolved)

	// EvalSymlinks the longest existing prefix, then re-append the
	// missing tail. This catches symlink bypass on create-new flows
	// (the file we're about to write doesn't exist yet, but its
	// parent — or some ancestor — may be a symlink into .git).
	resolved = evalSymlinksBestEffort(resolved)

	// Case-insensitive .git segment check — operators with macOS HFS+
	// or Windows NTFS would otherwise bypass via `.GIT/config`.
	for _, seg := range strings.Split(filepath.ToSlash(resolved), "/") {
		if strings.EqualFold(seg, ".git") {
			return fmt.Errorf("%w: %s", ErrGitMetadataWrite, path)
		}
	}
	return nil
}

// evalSymlinksBestEffort returns EvalSymlinks(p) when the path
// exists; otherwise walks up to the longest existing ancestor,
// EvalSymlinks that, and re-appends the missing tail. Returns
// `p` unchanged when EvalSymlinks can't make progress (every
// component is missing — typical for an absolute path that
// doesn't yet exist, e.g. `/tmp/new/foo.txt` where `/tmp` is
// the only existing piece). The point isn't perfect resolution
// — it's "follow any symlink that exists on the way down so a
// symlink-into-.git doesn't bypass the segment check."
func evalSymlinksBestEffort(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	// Walk up looking for the longest existing prefix.
	dir := p
	suffix := ""
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root; couldn't resolve any prefix. Fall back
			// to the lexical-clean form — better than nothing; the
			// segment check still catches the obvious `.git` token.
			return p
		}
		base := filepath.Base(dir)
		if suffix == "" {
			suffix = base
		} else {
			suffix = filepath.Join(base, suffix)
		}
		dir = parent
		if _, err := os.Stat(dir); err == nil {
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				return filepath.Join(real, suffix)
			}
		}
	}
}
