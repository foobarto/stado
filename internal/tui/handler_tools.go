package tui

// Tool + plugin event handlers. Per-tool result delivery, the tool
// elapsed-time tick, plugin approval / choice / fork / run-result
// notifications, and the toolsExecuted batch that closes a tool turn.

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

func onToolResult(m *Model, msg toolResultMsg) (tea.Model, tea.Cmd) {
	// Async tool call completed — result arrives here so the UI never
	// blocks on long-running tools (e.g. bash sleep 30).
	m.toolMu.Lock()
	if m.toolTickTimer != nil {
		m.toolTickTimer.Stop()
		m.toolTickTimer = nil
	}
	m.toolCancel = nil
	m.toolMu.Unlock()
	// Update the matching tool block with the result.
	toolName := ""
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == msg.result.ToolUseID {
			toolName = m.blocks[i].toolName
			m.blocks[i].toolResult = msg.result.Content
			m.invalidateBlockCache(i)
			break
		}
	}
	if toolName == "agent__spawn" && !msg.result.IsError {
		m.appendSubagentNotice(msg.result.Content)
	}
	m.pendingResults = append(m.pendingResults, msg.result)
	m.renderBlocks()
	return m, m.advanceToolQueue()
}

func onPluginApprovalRequest(m *Model, msg pluginApprovalRequestMsg) (tea.Model, tea.Cmd) {
	if m.approval != nil {
		select {
		case msg.response <- false:
		default:
		}
		return m, nil
	}
	m.approval = &approvalRequest{
		title:    msg.title,
		body:     msg.body,
		response: msg.response,
	}
	m.approvalFocused = false
	m.approvalAllowSelected = true
	m.state = stateApproval
	m.renderBlocks()
	return m, nil
}

func onPluginChoiceRequest(m *Model, msg pluginChoiceRequestMsg) (tea.Model, tea.Cmd) {
	if m.choice != nil || m.approval != nil {
		// Single-flight: drop the second request. Plugin sees
		// cancelled=true and decides what to do.
		select {
		case msg.response <- pluginRuntime.ChoiceResponse{Cancelled: true}:
		default:
		}
		return m, nil
	}
	m.choice = &choiceRequest{
		prompt:   msg.req.Prompt,
		options:  append([]pluginRuntime.ChoiceOption(nil), msg.req.Options...),
		multi:    msg.req.Multi,
		response: msg.response,
	}
	m.choiceCursor = 0
	m.choiceFocused = true
	m.choiceMarked = map[string]bool{}
	// Pre-toggle defaults. For single mode, the first id in Default
	// sets the cursor; for multi mode, every listed id starts toggled
	// on.
	if len(msg.req.Default) > 0 {
		if msg.req.Multi {
			for _, id := range msg.req.Default {
				m.choiceMarked[id] = true
			}
		} else {
			for i, opt := range m.choice.options {
				if opt.ID == msg.req.Default[0] {
					m.choiceCursor = i
					break
				}
			}
		}
	}
	m.state = stateChoice
	m.renderBlocks()
	return m, nil
}

func onPluginChoiceCancel(m *Model, msg pluginChoiceCancelMsg) (tea.Model, tea.Cmd) {
	if m.choice != nil && m.choice.response == msg.response {
		m.choice = nil
		m.choiceFocused = false
		m.choiceCursor = 0
		m.choiceMarked = nil
		m.state = stateIdle
		m.renderBlocks()
	}
	return m, nil
}

func onPluginApprovalCancel(m *Model, msg pluginApprovalCancelMsg) (tea.Model, tea.Cmd) {
	if m.approval != nil && m.approval.response == msg.response {
		m.approval = nil
		m.approvalFocused = false
		m.approvalAllowSelected = true
		m.state = stateIdle
		m.renderBlocks()
	}
	return m, nil
}

func onToolTick(m *Model, _ toolTickMsg) (tea.Model, tea.Cmd) {
	m.toolMu.Lock()
	running := m.toolCancel != nil
	m.toolMu.Unlock()
	if !running {
		return m, nil
	}
	// Re-render tool blocks so the elapsed-time counter ticks.
	m.renderBlocks()
	return m, m.toolTickCmd()
}

func onPluginRunResult(m *Model, msg pluginRunResultMsg) (tea.Model, tea.Cmd) {
	// /plugin:<name>-<ver> <tool> [args] finished. Render outcome as a
	// system block and leave conversation state untouched — plugin
	// invocations are side-channel and don't pollute the turn log the
	// LLM sees.
	if msg.errMsg != "" {
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf("plugin %s/%s error: %s", msg.plugin, msg.tool, msg.errMsg),
		})
	} else {
		m.appendBlock(block{
			kind: "system",
			body: fmt.Sprintf("plugin %s/%s → %s", msg.plugin, msg.tool, msg.content),
		})
	}
	m.renderBlocks()
	return m, nil
}

func onPluginFork(m *Model, msg pluginForkMsg) (tea.Model, tea.Cmd) {
	if m.recoveryPluginActive && msg.plugin == m.recoveryPluginName {
		return m, m.adoptForkedSession(msg.childID, msg.seed)
	}
	// A plugin's session:fork capability just created a child session.
	// DESIGN invariant 4: this is user-visible by default. Show both the
	// new session id + the fork point + a summary of the seed the
	// plugin wrote into the child's trace log.
	at := msg.atTurnRef
	if at == "" {
		at = "parent tree HEAD"
	}
	body := fmt.Sprintf("plugin %s forked session → %s  (at %s)", msg.plugin, msg.childID, at)
	if msg.seed != "" {
		body += "\n  seed: " + trimSeed(msg.seed, 120)
	}
	body += "\n  attach:  stado session attach " + msg.childID
	m.appendBlock(block{kind: "system", body: body})
	m.renderBlocks()
	return m, nil
}

func onToolsExecuted(m *Model, msg toolsExecutedMsg) (tea.Model, tea.Cmd) {
	m.annotateLastAssistantToolResults(msg.results)
	// EP-0037 lazy-load: when the model called tools.describe, parse
	// the result and add the described tools to this session's
	// activation set so subsequent turns surface them.
	m.absorbToolActivations(msg.results)
	// Append a role=tool message with the accumulated tool results.
	if len(msg.results) > 0 {
		blocks := make([]agent.Block, 0, len(msg.results))
		for _, r := range msg.results {
			cpy := r
			blocks = append(blocks, agent.Block{ToolResult: &cpy})
		}
		toolMsg := agent.Message{Role: agent.RoleTool, Content: blocks}
		m.msgs = append(m.msgs, toolMsg)
		m.persistMessage(toolMsg)
	}
	m.renderBlocks()
	return m, m.startStream()
}
