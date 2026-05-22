package acpwrap

// Default tool.Host for ACP-wrapped sessions when Tools = "stado".
// Auto-approves at the policy layer (stado-wide convention; tools
// enforce their own limits and bash relies on the sandbox, not
// Approve, for confinement) and exposes the sandbox.Runner so the
// bash tool's interface type-assert finds it. No read-dedup log:
// the wrapped agent's reads aren't part of stado's audit-aware turn
// loop, so dedup against stado's reads would be misleading.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/pkg/tool"
)

// DefaultHost is a minimal tool.Host suitable for the ACP-wrapped
// agent's tool calls. Use NewDefaultHost to construct.
type DefaultHost struct {
	workdir string
	runner  sandbox.Runner
}

// NewDefaultHost builds a DefaultHost rooted at workdir, exposing
// the supplied sandbox.Runner via Runner() so bash gets confined
// (the bash tool detects the runner via interface type-assert at
// internal/tools/bash/bash.go:50). When workdir is empty, the
// caller is responsible for setting it elsewhere — DefaultHost
// itself does not fall back to os.Getwd to avoid surprising
// path-resolution behaviour at construction time.
func NewDefaultHost(workdir string, runner sandbox.Runner) DefaultHost {
	return DefaultHost{workdir: workdir, runner: runner}
}

func (h DefaultHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (h DefaultHost) Workdir() string        { return h.workdir }
func (h DefaultHost) Runner() sandbox.Runner { return h.runner }
func (h DefaultHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (h DefaultHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

// CheckWritePath implements tool.WritePathGuard. The fs WriteTool/
// EditTool confine writes to Workdir() via workdirpath, but when the
// caller roots this Host at the real checkout (rather than a session
// worktree), a prompt-injected wrapped agent could still clobber
// repository metadata under .git/ inside that workdir. #050 calls
// this out specifically ("including repository metadata such as .git
// paths"). Until ACP fs/* writes are routed through the session
// worktree Executor (the full #050 fix, which lives in the TUI
// provider wiring outside this package), block writes that resolve
// into a .git directory as defense-in-depth — corrupting .git is the
// highest-impact write a wrapped agent can make and is never a
// legitimate fs/write_text_file target.
func (h DefaultHost) CheckWritePath(path string) error {
	// Resolve against the workdir the same way the fs tools do, so the
	// segment check sees the effective target rather than a relative
	// fragment.
	resolved := path
	if !filepath.IsAbs(resolved) && h.workdir != "" {
		resolved = filepath.Join(h.workdir, resolved)
	}
	resolved = filepath.Clean(resolved)
	for _, seg := range strings.Split(filepath.ToSlash(resolved), "/") {
		if seg == ".git" {
			return fmt.Errorf("acpwrap: refusing fs write into git metadata path %q (#050)", path)
		}
	}
	return nil
}
