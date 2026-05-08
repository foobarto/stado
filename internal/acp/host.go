package acp

import (
	"context"
	"errors"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
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
	server    *Server
	sessionID string
	workdir   string
	readLog   *tools.ReadLog
	runner    sandbox.Runner
}

func (h *acpHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h *acpHost) Workdir() string        { return h.workdir }
func (h *acpHost) Runner() sandbox.Runner { return h.runner }

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
