package main

import (
	"context"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/pkg/tool"
)

// pluginRunToolHost is the minimal tool.Host that `stado plugin run
// --with-tool-host` plugs into host.ToolHost so plugins importing
// bundled tools (stado_http_get, stado_fs_tool_*, stado_lsp_*,
// stado_search_*) can run end-to-end from the CLI.
//
// What it provides:
//
//   - Workdir() — the path the operator picked via --workdir (or the
//     plugin install dir if --workdir is unset). Bundled tools that
//     resolve relative paths against the host's workdir use this.
//   - Approve() → DecisionAllow — single-shot CLI invocation; the
//     operator authorised the call by typing the command. Note that
//     this is NOT a substitute for runtime capability gates — those
//     are enforced by the wasm host imports against the manifest's
//     declared capabilities, regardless of approval. EP-0005 §"Goals".
//   - Runner() (sandbox.Runner) — the same `sandbox.Detect()` runner
//     the agent loop uses (bwrap on Linux, sandbox-exec on macOS,
//     NoneRunner elsewhere). Bash duck-types this to wrap commands in
//     the platform sandbox. plugin_run.go refuses to start when the
//     manifest declares `exec:bash` AND `Detect()` returns NoneRunner —
//     that's the "approval ≠ policy" guard from EP-0005: we don't
//     substitute the operator's CLI invocation for a real syscall
//     filter when none is available.
//   - PriorRead/RecordRead — no-op. The agent-loop dedup machinery
//     doesn't apply to one-shot plugin invocations.
type pluginRunToolHost struct {
	workdir string
	runner  sandbox.Runner
}

func newPluginRunToolHost(workdir string, runner sandbox.Runner) tool.Host {
	return pluginRunToolHost{workdir: workdir, runner: runner}
}

func (h pluginRunToolHost) Workdir() string { return h.workdir }

func (h pluginRunToolHost) Runner() sandbox.Runner { return h.runner }

func (h pluginRunToolHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h pluginRunToolHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}

func (h pluginRunToolHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}
