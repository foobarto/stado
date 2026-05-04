package main

import (
	"context"

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
//   - PriorRead/RecordRead — no-op. The agent-loop dedup machinery
//     doesn't apply to one-shot plugin invocations.
//
// What it deliberately does NOT provide:
//
//   - Runner() (sandbox.Runner) — bash uses this duck-typed extension
//     to run commands inside a Landlock/Seatbelt sandbox. Returning a
//     no-op runner here would mean bash runs unsandboxed, contrary to
//     EP-0005 §"Non-goals" ("Treating human approval as a substitute
//     for kernel or runtime policy"). Instead, plugin_run.go refuses
//     any plugin that declares `exec:bash` under --with-tool-host
//     before it gets here.
type pluginRunToolHost struct {
	workdir string
}

func newPluginRunToolHost(workdir string) tool.Host {
	return pluginRunToolHost{workdir: workdir}
}

func (h pluginRunToolHost) Workdir() string { return h.workdir }

func (h pluginRunToolHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h pluginRunToolHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}

func (h pluginRunToolHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}
