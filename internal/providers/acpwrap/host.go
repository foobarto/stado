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
func (h DefaultHost) Workdir() string                                  { return h.workdir }
func (h DefaultHost) Runner() sandbox.Runner                           { return h.runner }
func (h DefaultHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) { return tool.PriorReadInfo{}, false }
func (h DefaultHost) RecordRead(tool.ReadKey, tool.PriorReadInfo)       {}
