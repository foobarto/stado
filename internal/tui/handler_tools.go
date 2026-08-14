package tui

// Tool + plugin event handlers. Per-tool result delivery, the tool
// elapsed-time tick, plugin approval / choice / fork / run-result
// notifications, and the toolsExecuted batch that closes a tool turn.

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/supervise"
	"github.com/foobarto/stado/internal/textutil"
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
	// Sanitize at store-time so every downstream renderer can trust the
	// stored bytes. Cluster C1 P1 — sibling miss to PR #49: streaming
	// assistant/thinking text is sanitized at the append boundary
	// (model_stream.go EvTextDelta/EvThinkingDelta), but the tool-result
	// seam stored msg.result.Content verbatim, so a tool whose output
	// carried OSC 0 (title rewrite), OSC 8 (clickable hyperlink), BEL, or
	// CSI could drive those straight into the tool panel's lipgloss render.
	// SanitizeForTerminal keeps legitimate \n / \t / \r (tool output is
	// multi-line prose) while stripping the escape vectors. The same
	// sanitized content feeds appendSubagentNotice, whose JSON fields
	// (error / worktree / child session) are rendered into a system block —
	// sanitizing first preserves valid JSON structure while scrubbing any
	// escape inside the string values.
	content := textutil.SanitizeForTerminal(msg.result.Content)
	// Update the matching tool block with the result.
	toolName, toolArgs := "", ""
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == msg.result.ToolUseID {
			toolName = m.blocks[i].toolName
			toolArgs = m.blocks[i].toolArgs
			// skills__load (EP-0045): the tool panel only needs the short
			// confirmation, but the full body-bearing result must survive in
			// pendingResults so the batch handler (onToolsExecuted →
			// absorbSkillLoads) can inject the body as a user message — same
			// effect as a user `/skill:`. So trim for DISPLAY only; persist
			// the raw result below. (The prior bug trimmed pendingResults at
			// store-time, discarding the body before injection — model
			// invocation was a TUI no-op.)
			display := content
			if toolName == "skills__load" && !msg.result.IsError {
				if _, trimmed := runtime.AbsorbSkillLoad(content); trimmed != content {
					display = trimmed
				}
			}
			m.blocks[i].toolResult = display
			m.blocks[i].streaming = false // result in hand — auto mode collapses it
			m.invalidateBlockCache(i)
			break
		}
	}
	if toolName == "agent__spawn" && !msg.result.IsError {
		m.appendSubagentNotice(content)
	}
	if strings.HasPrefix(toolName, "supervise__") {
		m.syncSuperviseState()
		if m.supervision != nil && !msg.result.IsError {
			switch {
			case toolName == supervisePivotTool && m.supervision.state.Status == supervise.StatusPivotPending:
				m.recordSuperviseControlEvent(supervise.WorkerPivotRequested)
			case toolName == superviseCompletionTool && m.supervision.state.Status == supervise.StatusVerifying:
				m.recordSuperviseControlEvent(supervise.WorkerCompletionClaimed)
			}
		}
	}
	var observe []tea.Cmd
	// Successful pivot/completion control calls bind their pending request to
	// the current anchor and change status. Do not advance that anchor again
	// while recording the tool result, or every later verdict becomes stale.
	if m.supervision != nil && m.supervision.state.Status == supervise.StatusRunning {
		beforeTree := m.supervision.state.TreeDigest
		outcome := supervise.WorkerEvent{
			Kind:          supervise.WorkerToolOutcome,
			Tool:          toolName,
			ArgsDigest:    digestBytes([]byte(toolArgs)),
			OutcomeDigest: digestBytes([]byte(content)),
			Succeeded:     !msg.result.IsError,
		}
		if msg.result.IsError {
			outcome.ErrorFingerprint = digestBytes([]byte(strings.TrimSpace(content)))
		}
		observe = append(observe, m.observeSupervise(outcome))
		switch toolName {
		case superviseProgressTool:
			var progress struct {
				StepComplete bool `json:"step_complete"`
			}
			if !msg.result.IsError && json.Unmarshal([]byte(toolArgs), &progress) == nil && progress.StepComplete {
				observe = append(observe, m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerStepClaimed}))
			}
		case supervisePivotTool:
			observe = append(observe, m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerPivotRequested}))
		case superviseCompletionTool:
			// A rejected request (for example, while plan steps remain) is
			// exactly the unsupported-completion signal the watchdog should see.
			observe = append(observe, m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerCompletionClaimed}))
		}
		afterTree := m.superviseTreeDigest()
		if afterTree != beforeTree {
			patch, paths := m.superviseDiffSnapshot(m.supervision.state)
			observe = append(observe, m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerTreeChanged, TreeDigest: afterTree, DiffBytes: int64(len(patch)), ChangedPaths: paths}))
		}
	}
	// Persist the raw result (faithful tool output for the model + audit) —
	// absorbSkillLoads trims any skills__load body in onToolsExecuted, after
	// extracting it for injection.
	m.pendingResults = append(m.pendingResults, msg.result)
	m.renderBlocks()
	observe = append(observe, m.advanceToolQueue())
	return m, tea.Batch(observe...)
}

func onPluginApprovalRequest(m *Model, msg pluginApprovalRequestMsg) (tea.Model, tea.Cmd) {
	if m.approval != nil {
		select {
		case msg.response <- false:
		default:
		}
		return m, nil
	}
	// Sanitize at store-time so the approval drawer renderer can trust
	// what's in m.approval. Codex C2/J-c P1 — PR #49 sibling miss:
	// stado_ui_approve flowed plugin-controlled title/body straight to
	// lipgloss.Render at approval.go:45/47, so a malicious or buggy
	// plugin could emit OSC52 / OSC8 / CSI in the approval dialog. The
	// title is shown as a single bold header line → StripControlChars.
	// The body is rendered as a multi-line description → SanitizeForTerminal
	// keeps legitimate \n / \t formatting.
	m.approval = &approvalRequest{
		title:    textutil.StripControlChars(msg.title),
		body:     textutil.SanitizeForTerminal(msg.body),
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
	// F10: seed per-option input values from each option's
	// Input.Default. Non-input options stay at "" so the slice
	// remains index-aligned with options.
	m.choiceInputs = make([]string, len(msg.req.Options))
	for i, opt := range msg.req.Options {
		if opt.Input != nil {
			m.choiceInputs[i] = opt.Input.Default
		}
	}
	m.choiceValidationErr = ""
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
		m.choiceInputs = nil
		m.choiceValidationErr = ""
		m.state = stateIdle
		m.renderBlocks()
	}
	return m, nil
}

// onPluginPrint handles a stado_ui_print fire-and-forget emit (F9a).
// Append a system block with a severity prefix when one was set so
// warn / error stand out without the renderer needing to know about
// the per-emit metadata. stream_id is preserved on the wire but the
// F9a slice does not coalesce — F9b lands proper continuation
// rendering.
func onPluginPrint(m *Model, msg pluginPrintMsg) (tea.Model, tea.Cmd) {
	body := msg.text
	switch msg.opts.Severity {
	case "warn":
		body = "[warn] " + body
	case "error":
		body = "[error] " + body
	}
	m.appendBlock(block{kind: "system", body: body})
	m.renderBlocks()
	return m, nil
}

// onPluginRender handles a stado_ui_render fire-and-forget panel emit
// (F9b.2). The decoded Panel is rendered to a multi-line bordered
// ASCII string by renderPanelASCII (panel_render.go), then appended
// as a system block. Per-channel render variations (TUI styling
// upgrades, colour per Variant) can layer on later without changing
// this handler's contract — the system block is just text by the
// time it arrives.
//
// Per spec, render is fire-and-forget: no response, no state change
// beyond appending the block. Variant colour styling is a future
// enhancement; today the variant is shown as a parenthetical in the
// title bar so warn / error / etc. still surface visually without
// requiring theme integration.
func onPluginRender(m *Model, msg pluginRenderMsg) (tea.Model, tea.Cmd) {
	// #21 part 2: route by display target. Empty (legacy emits) and
	// "viewport" keep the original scrollback-block behaviour; the other
	// targets store the panel for the sidebar/footer/log to surface.
	//
	// The chrome targets do NOT call renderBlocks(): that rebuilds the
	// conversation-viewport cache, which is unchanged here. View() re-runs
	// renderSidebar / renderStatus / the log tail fresh every frame and reads
	// these stores directly, so the next repaint surfaces the update without
	// re-rendering the whole conversation history (cheap even for a chatty
	// plugin updating panels every tick).
	switch msg.panel.Target {
	case "sidebar":
		// Plugins may not write to built-in sections — drop silently so a
		// plugin can never shadow native chrome (decision: built-ins are
		// read-only to plugins).
		if isBuiltinSidebarSection(msg.panel.ID) {
			break
		}
		if m.pluginSidebarPanels == nil {
			m.pluginSidebarPanels = map[string]pluginRuntime.Panel{}
		}
		// Cap distinct-id growth (a new id past the cap is dropped);
		// last-write-wins on an already-stored id is always allowed.
		if _, exists := m.pluginSidebarPanels[msg.panel.ID]; !exists && len(m.pluginSidebarPanels) >= maxPluginChromePanels {
			break
		}
		m.pluginSidebarPanels[msg.panel.ID] = msg.panel // last-write-wins per id
	case "footer":
		if isBuiltinFooterSegment(msg.panel.ID) {
			break
		}
		// Only store if there's a renderable short line. (decodeRenderRequest
		// requires a non-empty Title, so this guard is defensive — a decoded
		// panel always yields text — but it keeps direct callers honest.)
		if pluginFooterText(msg.panel) == "" {
			break
		}
		if m.pluginFooterPanels == nil {
			m.pluginFooterPanels = map[string]pluginRuntime.Panel{}
		}
		if _, exists := m.pluginFooterPanels[msg.panel.ID]; !exists && len(m.pluginFooterPanels) >= maxPluginChromePanels {
			break
		}
		m.pluginFooterPanels[msg.panel.ID] = msg.panel // last-write-wins per id
	case "log":
		m.pushLogLine(pluginLogLine(msg.panel))
	default: // "" / "viewport"
		body := renderPanelASCII(msg.panel)
		m.appendBlock(block{kind: "system", body: body})
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
	// /tool <name> and /plugin:<name>-<ver> <tool> [args] finished.
	// Render the outcome as a collapsible TOOL block (kind:"tool"), not a
	// system block: the result body flows through the fixed-height
	// clip-panel (clipToolOutput + the "… N more line(s)" footer) the
	// agent-loop tool calls already use, so a large tool result no longer
	// floods scrollback line-for-line. Conversation state stays untouched
	// — these invocations are side-channel and don't pollute the turn log
	// the LLM sees.
	//
	// Errors carry a short message that fits the collapsed budget, so
	// they stay fully visible without the operator having to expand.
	name := msg.tool
	if msg.plugin != "" {
		name = msg.plugin + "/" + msg.tool
	}
	result := msg.content
	if msg.errMsg != "" {
		result = "error: " + msg.errMsg
	}
	// Sanitize the plugin/tool-supplied name + result (same hostile-bytes
	// trust boundary as agent-loop tool results — see onToolResult): they
	// render raw into the kind:"tool" panel, so an OSC/BEL here would rewrite
	// the terminal title, inject a clickable hyperlink, or ring the bell.
	m.appendBlock(block{
		kind:       "tool",
		toolName:   textutil.SanitizeForTerminal(name),
		toolResult: textutil.SanitizeForTerminal(result),
	})
	m.renderBlocks()
	return m, nil
}

func onPluginFork(m *Model, msg pluginForkMsg) (tea.Model, tea.Cmd) {
	if m.recoveryPluginActive && msg.plugin == m.recoveryPluginName {
		return m, m.adoptForkedSession(msg.childID, msg.atTurnRef, msg.seed)
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
	// Codex validated finding (post-#46): when the operator pressed
	// a kill-switch key during this turn (Esc/Ctrl+G, Alt+Enter,
	// /cancel, /queue-now, /force, /stop), the cancelled tool's
	// `context.Canceled` came back as a normal toolResultMsg, the
	// (now-empty) queue drained, and this handler unconditionally
	// re-started the provider stream — letting the model request
	// another tool and continue the turn the operator just stopped.
	// Refuse the re-stream when the cancellation flag is set;
	// dispatch any queued operator prompt instead so the operator's
	// next-turn intent still fires.
	if m.turnCancelled {
		m.turnCancelled = false
		// #16: a steering message belonged to the turn the operator just
		// cancelled — drop it so it can't inject into the next turn.
		m.steeringMsg = ""
		// Conversation-history invariant (Copilot review on Cluster R
		// round 1): the assistant message containing the tool_use
		// blocks was already persisted by onTurnComplete BEFORE this
		// gate fires. If we don't also persist the matching
		// tool_result blocks, the message history has an orphan
		// tool_use → providers like OpenAI Chat Completion reject the
		// next turn as malformed. So we DO append the role=tool
		// message; we just don't re-stream the model. The results are
		// real ("cancelled by user" errors from the goroutine), so
		// the audit log + history reflect what actually happened —
		// the operator just doesn't get an autonomous follow-up.
		if len(msg.results) > 0 {
			// Trim any skills__load body out of the persisted tool result
			// (the operator cancelled, so we never inject it) — but keep
			// history well-formed so the next turn isn't an orphan tool_use.
			m.absorbSkillLoads(msg.results)
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
		if m.supervision != nil && (m.supervision.interventionHold || m.supervision.state.PendingIntervention != nil) {
			return m, m.nextSuperviseHostAction()
		}
		if m.state == stateIdle && m.queuedPrompt != "" {
			return m, m.promoteQueuedPrompt()
		}
		return m, nil
	}
	m.annotateLastAssistantToolResults(msg.results)
	injections := m.absorbSkillLoads(msg.results)
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
	for _, body := range injections {
		userMsg := agent.Text(agent.RoleUser, body)
		m.msgs = append(m.msgs, userMsg)
		m.appendBlock(block{kind: "user", body: body})
		m.persistMessage(userMsg)
	}
	if len(msg.results) > 0 && m.broker != nil {
		if err := m.broker.SetTaint(m.rootCtx, runtime.ContextTainted); err != nil {
			if m.supervision != nil && !superviseTerminal(m.supervision.state.Status) {
				m.pauseSupervisionAfterTaintFailure(m.supervision, "tool-result continuation", err)
				return m, nil
			}
			m.state = stateError
			m.errorMsg = err.Error()
			m.appendBlock(block{kind: "system", body: "broker taint update failed: " + err.Error()})
			m.renderBlocks()
			return m, nil
		}
	}
	if m.supervision != nil {
		m.syncSuperviseState()
		if m.supervision.interventionHold || m.supervision.state.PendingIntervention != nil {
			m.supervision.interventionHold = true
			m.renderBlocks()
			return m, m.nextSuperviseHostAction()
		}
		if m.supervision.state.Status == supervise.StatusVerifying {
			m.renderBlocks()
			return m, m.startSuperviseCompletionFlow()
		}
		if m.supervision.state.Status == supervise.StatusPivotPending {
			m.renderBlocks()
			return m, m.startSupervisePivotReview()
		}
		if len(m.supervision.followupQueue) > 0 {
			// The agent loop would normally continue after tool results. Preserve
			// that continuation while the host temporarily classifies the inbox.
			m.supervision.followupResume = true
			m.renderBlocks()
			return m, m.startNextSuperviseFollowupReview()
		}
	}
	// #16: this is the "next opportunity" — a steering message injects
	// here so the model's next round-trip sees it alongside the results.
	m.drainSuperviseAdvisorySteering()
	m.drainSteering()
	m.renderBlocks()
	if command := m.applicationTurnBoundary(msg.results, applicationBoundaryContinueTools); command != nil {
		return m, command
	}
	return m, m.startStream()
}
