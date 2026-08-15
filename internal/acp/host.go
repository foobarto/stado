package acp

import (
	"context"
	"errors"
	"fmt"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// acpHost is the tool.Host the ACP server hands to the agent loop.
// Auto-approves tool calls like the loop's default host (no operator-side
// tool-call approval gate in ACP — clients pre-authorise those), and
// additionally exposes RequestChoice + RequestApproval so wasm plugins
// importing stado_ui_choose / stado_ui_approve see the operator through
// session/update kind=choice|approval instead of "interactive UI
// unavailable". Q3 Phase B + approval-bridge follow-up.
type acpHost struct {
	server          *Server
	sessionID       string
	workdir         string
	readLog         *tools.ReadLog
	runner          sandbox.Runner
	executorSandbox runtime.ExecutorSandbox
	provider        agent.Provider
	defaultModel    string
}

func (h *acpHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h *acpHost) Workdir() string        { return h.workdir }
func (h *acpHost) Runner() sandbox.Runner { return h.runner }

func (h *acpHost) PluginProviderBridge(identityCanonical string) (pluginRuntime.ProviderBridge, error) {
	if identityCanonical == "" || h.provider == nil {
		return nil, errors.New("ACP provider bridge unavailable")
	}
	return &pluginRuntime.NativeProviderBridge{Provider: h.provider, DefaultModel: h.defaultModel}, nil
}

// DefaultSandboxPolicy makes acp an AUTONOMOUS surface for tool execution: a
// Zed / ACP client drives the agent, not the operator at a terminal, so bash
// (`shell.exec`) tool calls run under the protective default sandbox policy by
// default — matching mcp-server / daemon, and closing the gap where acp's bash
// previously ran unconfined. Model A (decision 2026-06-13). Returns the
// *sandboxPolicy via `any` per tool.SandboxPolicyProvider; the wasm guest can
// only TIGHTEN it (intersectPolicies), never widen.
func (h *acpHost) DefaultSandboxPolicy() any {
	return h.executorSandbox.DefaultSandboxPolicy(h.workdir)
}

func (h *acpHost) SpawnSubagent(_ context.Context, _ subagent.Request) (subagent.Result, error) {
	return subagent.Result{}, errors.New("spawn_agent unavailable: ACP host has no subagent fleet")
}

func (h *acpHost) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	if h.readLog == nil {
		return tool.PriorReadInfo{}, false
	}
	return h.readLog.PriorRead(key)
}

func (h *acpHost) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	if h.readLog == nil {
		return
	}
	h.readLog.RecordRead(key, info)
}

// RequestChoice routes through the ACP server's pending-choice
// registry. Implements pluginRuntime.ChoiceBridge — picked up by
// pluginrun's attachLifecycleBridges via interface assertion.
//
// F10 ACP follow-on (2026-05-08): per-option `prefix` and `input`
// fields now flow through `session/update kind=choice`, and the
// response shape carries `inputValue`. The TUI-only rejection guard
// is gone; ACP clients that don't yet handle input render the
// option's `label` and ignore the input metadata, which is a
// graceful degradation.
//
// Multi-select with input fields is still rejected here — same
// reason as the TUI requestPluginChoice gate: the semantics of
// typing into N rows of a multi-select are unsolved and the F10
// spec doesn't address the combo. Plugins should pick one or the
// other.
func (h *acpHost) RequestChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	if h.server == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("acp host has no server reference")
	}
	if req.Multi {
		for _, o := range req.Options {
			if o.Input != nil {
				return pluginRuntime.ChoiceResponse{}, errors.New("multi-select choice with per-option input fields is not supported")
			}
		}
	}
	return h.server.requestChoice(ctx, h.sessionID, req)
}

// RequestApproval routes through the ACP server's pending-approval
// registry. Implements pluginRuntime.ApprovalBridge — picked up by
// pluginrun's attachLifecycleBridges via interface assertion. With
// this in place, plugins calling stado_ui_approve over ACP get the
// operator's verdict instead of -1 unavailable.
func (h *acpHost) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	if h.server == nil {
		return false, errors.New("acp host has no server reference")
	}
	return h.server.requestApproval(ctx, h.sessionID, title, body)
}

// CheckWritePath implements pkg/tool.WritePathGuard so the fs.write
// tool refuses operations that resolve into a .git directory.
//
// Codex K P0 — pre-fix the WritePathGuard interface was implemented
// only on internal/providers/acpwrap/host.go's DefaultHost. The ACP
// SERVER side host here (the *acpHost the ACP server hands to the
// agent loop when handling tool-call RPCs from a wrapped agent) did
// NOT implement it, so fs.write through this host bypassed the
// .git-write guard that #050 + acpwrap had put in place. Copy the
// same defense from acpwrap.DefaultHost.CheckWritePath verbatim:
// resolve against the host workdir, walk path segments, refuse any
// `.git` segment match. The mutation has to be refused at this
// layer because by the time the fs.write tool runs there's no
// per-call cancel hook that could undo a partial write.
func (h *acpHost) CheckWritePath(path string) error {
	if err := tool.DefaultGitWritePathGuard(h.workdir, path); err != nil {
		return fmt.Errorf("acp host: %w (Codex K)", err)
	}
	return nil
}
