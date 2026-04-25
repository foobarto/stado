package subagent

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/tool"
)

// ScopedWriteHost wraps a tool host with write-path enforcement for the future
// workspace_write subagent mode. It does not grant any tools by itself.
type ScopedWriteHost struct {
	tool.Host
	writeScope []string
}

var _ tool.WritePathGuard = ScopedWriteHost{}

// NewScopedWriteHost returns a host wrapper that permits writes only within
// writeScope. The scope must already be explicit; an empty scope is rejected.
func NewScopedWriteHost(base tool.Host, writeScope []string) (ScopedWriteHost, error) {
	if base == nil {
		return ScopedWriteHost{}, fmt.Errorf("base host is required")
	}
	normalized, err := NormalizeWriteScope(writeScope)
	if err != nil {
		return ScopedWriteHost{}, err
	}
	if len(normalized) == 0 {
		return ScopedWriteHost{}, fmt.Errorf("write_scope is required")
	}
	return ScopedWriteHost{Host: base, writeScope: normalized}, nil
}

// WriteScope returns the normalized scope enforced by this host.
func (h ScopedWriteHost) WriteScope() []string {
	out := make([]string, len(h.writeScope))
	copy(out, h.writeScope)
	return out
}

// CheckWritePath rejects any target outside the normalized write scope.
func (h ScopedWriteHost) CheckWritePath(target string) error {
	if h.Host == nil {
		return fmt.Errorf("write_scope host is unavailable")
	}
	rel, err := normalizedWriteTarget(h.Workdir(), target)
	if err != nil {
		return err
	}
	if hasPathSegment(rel, ".git") {
		return fmt.Errorf("write path %q targets .git metadata", target)
	}
	if hasPathSegment(rel, ".stado") {
		return fmt.Errorf("write path %q targets .stado metadata", target)
	}
	for _, scope := range h.writeScope {
		if writeScopeMatches(scope, rel) {
			return nil
		}
	}
	return fmt.Errorf("write path %q is outside write_scope", target)
}

func normalizedWriteTarget(workdir, target string) (string, error) {
	full, err := workdirpath.Resolve(workdir, target, true)
	if err != nil {
		return "", err
	}
	root, err := workdirpath.Resolve(workdir, ".", false)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", fmt.Errorf("write path %q resolves to the repository root", target)
	}
	return rel, nil
}

func writeScopeMatches(scope, target string) bool {
	if scope == target {
		return true
	}
	if !containsGlobMeta(scope) {
		return strings.HasPrefix(target, scope+"/")
	}
	return matchScopeSegments(strings.Split(scope, "/"), strings.Split(target, "/"))
}

func containsGlobMeta(scope string) bool {
	return strings.ContainsAny(scope, "*?[")
}

func matchScopeSegments(pattern, target []string) bool {
	if len(pattern) == 0 {
		return len(target) == 0
	}
	if pattern[0] == "**" {
		if matchScopeSegments(pattern[1:], target) {
			return true
		}
		for i := range target {
			if matchScopeSegments(pattern[1:], target[i+1:]) {
				return true
			}
		}
		return false
	}
	if len(target) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], target[0])
	if err != nil || !ok {
		return false
	}
	return matchScopeSegments(pattern[1:], target[1:])
}
