package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// CeilingRunner is a Runner decorator that intersects every per-call
// Policy with a fixed ceiling Policy before delegating to the inner
// Runner. Used by the v1 broker-attach path in `stado run` (and
// equivalents) to ensure that the broker's projected ceiling for the
// session bounds what any individual tool call can request — even if
// a per-call Policy is wider, the intersection guarantees we don't
// exceed the ceiling.
//
// Composition rules follow Policy.Merge (intersection-only). The
// ceiling itself is immutable for the life of the runner.
//
// Inner must be non-nil. A nil inner Runner is treated as NoneRunner
// (defensive — should not happen in practice).
type CeilingRunner struct {
	Inner   Runner
	Ceiling Policy
}

// NewCeilingRunner wraps inner with a ceiling. If ceiling is the
// zero value (no constraints) the wrapper is functionally
// equivalent to inner — every per-call Policy passes through
// unchanged. If inner is nil, NoneRunner{} is used so the returned
// runner never panics.
func NewCeilingRunner(inner Runner, ceiling Policy) *CeilingRunner {
	if inner == nil {
		inner = NoneRunner{}
	}
	return &CeilingRunner{Inner: inner, Ceiling: ceiling}
}

// Name returns the inner runner's name with a "+ceiling" suffix
// so logs and the announcement banner can distinguish a ceiling-
// wrapped runner from a bare one.
func (r *CeilingRunner) Name() string {
	if r == nil || r.Inner == nil {
		return "none+ceiling"
	}
	return r.Inner.Name() + "+ceiling"
}

// Available delegates to the inner runner. The wrapping is purely
// behavioral; the inner runner's availability gates the whole.
func (r *CeilingRunner) Available() bool {
	if r == nil || r.Inner == nil {
		return false
	}
	return r.Inner.Available()
}

// Command validates CWD against the ceiling, intersects p with the ceiling,
// and passes the result to the inner runner. The resulting sandbox cannot
// expand beyond the session grant even through an implicit CWD bind.
//
// When the ceiling is the zero value (no constraints), the
// intersection is just p — wrapping is then a no-op decorator.
// This is the right behavior for ProfileNoSandbox.
func (r *CeilingRunner) Command(ctx context.Context, p Policy, name string, args, env []string) (*Command, error) {
	if r == nil || r.Inner == nil {
		return NoneRunner{}.Command(ctx, p, name, args, env)
	}
	// Skip intersection when the ceiling is empty — the per-call
	// Policy is the only bound. Avoids producing an empty Policy
	// from intersecting a real per-call set with the empty ceiling,
	// which would deny everything by mistake.
	if isZeroPolicy(r.Ceiling) {
		return r.Inner.Command(ctx, p, name, args, env)
	}
	if p.CWD != "" {
		readable := append(append([]string(nil), r.Ceiling.FSRead...), r.Ceiling.FSWrite...)
		if !resolvedPathWithinAny(p.CWD, readable) {
			return nil, fmt.Errorf("sandbox: cwd %q exceeds broker ceiling", p.CWD)
		}
	}
	merged := p.Merge(r.Ceiling)
	return r.Inner.Command(ctx, merged, name, args, env)
}

// resolvedPathWithinAny checks an execution-time path against allowed roots
// after resolving symlinks. Broker policy projection is lexical, but the
// runner must validate the actual target before bind-mounting a CWD.
func resolvedPathWithinAny(path string, roots []string) bool {
	resolvedPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return false
	}
	for _, root := range roots {
		root = strings.TrimSuffix(filepath.Clean(root), string(filepath.Separator)+"...")
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isZeroPolicy reports whether p is the zero value (no FSRead /
// FSWrite / Exec / Env / Net constraints). Used by CeilingRunner
// to detect the "no constraints" case and skip the intersection.
func isZeroPolicy(p Policy) bool {
	return len(p.FSRead) == 0 &&
		len(p.FSWrite) == 0 &&
		p.Exec == nil &&
		len(p.Env) == 0 &&
		len(p.Mask) == 0 &&
		p.Net.Kind == NetDenyAll && // NB: zero value of NetKind is NetDenyAll
		len(p.Net.Hosts) == 0 &&
		p.CWD == "" &&
		p.Timeout == 0
}
