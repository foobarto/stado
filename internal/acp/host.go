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
// Auto-approves like the loop's default host (no operator-side approval
// gate in ACP — clients pre-authorise tool calls), and additionally
// exposes RequestChoice so wasm plugins importing stado_ui_choose see
// the operator through session/update kind=choice instead of
// "interactive UI unavailable". Q3 Phase B.
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
func (h *acpHost) RequestChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	if h.server == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("acp host has no server reference")
	}
	return h.server.requestChoice(ctx, h.sessionID, req)
}
