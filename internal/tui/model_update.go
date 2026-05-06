package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tui/filepicker"
	"github.com/foobarto/stado/internal/tui/fleetpicker"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/pkg/agent"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.slash.Width = msg.Width
		m.layout()
		return m, nil

	case titleTickMsg:
		// Animated terminal-tab title — see title_spinner.go.
		return m, m.handleTitleTick()

	case streamEventMsg:
		m.handleStreamEvent(msg.ev)
		m.renderBlocks()
		return m, nil

	case streamBatchMsg:
		for _, ev := range msg.evs {
			m.handleStreamEvent(ev)
		}
		m.renderBlocks()
		return m, nil

	case streamTickMsg:
		if m.state != stateStreaming {
			return m, nil
		}
		// Drain the shared stream buffer. Throttle the actual
		// renderBlocks() to at most once every 100ms so bubbletea's
		// renderer doesn't choke under reasoning-model event rates.
		// Without this, each tick (50ms) renders the whole viewport
		// — 10+ renders/sec of ANSI-heavy markdown content starves
		// the keyboard reader on bubbletea's unbuffered message
		// channel. Terminal events (seen inside batch) force an
		// immediate render so the final state is never stale.
		m.streamBufMu.Lock()
		batch := m.streamBuf
		m.streamBuf = nil
		closed := m.streamBufClosed
		m.streamBufMu.Unlock()
		boundary := false
		for _, ev := range batch {
			m.handleStreamEvent(ev)
			if ev.Kind == agent.EvDone || ev.Kind == agent.EvError ||
				ev.Kind == agent.EvToolCallStart || ev.Kind == agent.EvToolCallEnd {
				boundary = true
			}
		}
		if len(batch) > 0 && (boundary || time.Since(m.lastStreamRender) > 100*time.Millisecond) {
			m.renderBlocks()
			m.lastStreamRender = time.Now()
		}
		if closed {
			return m, func() tea.Msg { return streamDoneMsg{} }
		}
		return m, streamTickCmd()

	case streamErrorMsg:
		m.state = stateError
		m.errorMsg = msg.err.Error()
		m.streamCancel = nil
		m.streamBufMu.Lock()
		m.streamBufClosed = true
		m.streamBufMu.Unlock()
		m.appendBlock(block{kind: "system", body: "error: " + msg.err.Error()})
		m.renderBlocks()
		return m, nil

	case logTailMsg:
		m.recordLogLine(msg.line)
		return m, nil

	case localFallbackReadyMsg:
		m.providerProbePending = false
		if m.provider == nil && msg.provider != nil {
			m.provider = msg.provider
		}
		if m.providerName == "" && msg.providerName != "" {
			m.providerName = msg.providerName
		}
		if m.model == "" && len(msg.models) > 0 {
			m.model = msg.models[0]
		}
		if msg.provider != nil {
			tuiTrace("startup provider probe resolved",
				"provider", msg.providerName,
				"models", len(msg.models),
				"queued_prompt", m.queuedPrompt != "")
			if m.state == stateIdle && m.queuedPrompt != "" {
				m.renderBlocks()
				return m, m.promoteQueuedPrompt()
			}
			return m, nil
		}
		tuiTrace("startup provider probe found no fallback", "queued_prompt", m.queuedPrompt != "")
		if m.state == stateIdle && m.queuedPrompt != "" {
			queued := m.restoreQueuedPromptToInput()
			m.state = stateError
			m.errorMsg = noProviderConfiguredError().Error()
			m.appendBlock(block{
				kind: "system",
				body: "Provider unavailable: " + noProviderConfiguredError().Error() +
					"\n\nYour draft was restored to the input box: " + trimSeed(queued, 48),
			})
			m.renderBlocks()
		}
		return m, nil

	case streamDoneMsg:
		m.streamCancel = nil
		if m.state == stateError {
			return m, nil
		}
		m.maybeEmitBudgetWarning()
		m.firePostTurnHook()
		var cmds []tea.Cmd
		cmds = append(cmds, m.onTurnComplete(), m.tickBackgroundPluginsWithEvent(m.turnCompleteEvent()))
		// EP-0036: after each turn, check if the loop agent signalled
		// done; if not and loop is active, queue the next iteration or
		// schedule the next tick.
		if m.loop != nil {
			lastText := m.lastAssistantText()
			if !m.loopCheckDone(lastText) {
				if m.loop.interval > 0 {
					cmds = append(cmds, m.loopTick())
				} else {
					cmds = append(cmds, m.loopIterate())
				}
			}
		}
		return m, tea.Batch(cmds...)

	case loopTickMsg:
		// EP-0036: timed loop interval elapsed — start next iteration if idle.
		if m.loop != nil && m.state != stateStreaming {
			return m, m.loopIterate()
		}
		// If busy, reschedule — the turn-done path will call loopTick again.
		return m, nil

	case monitorLinesMsg:
		// EP-0036: batch of monitor output lines delivered to the session.
		for _, line := range msg {
			m.appendBlock(block{kind: "system", body: "[monitor] " + line})
		}
		m.renderBlocks()
		return m, nil

	case monitorDoneMsg:
		// EP-0036: monitored process exited.
		if m.monitor != nil {
			m.monitor = nil
		}
		body := "monitor: process exited"
		if msg.err != nil {
			body += " (" + msg.err.Error() + ")"
		}
		m.appendBlock(block{kind: "system", body: body})
		m.renderBlocks()
		return m, nil

	case backgroundTickResultMsg:
		m.backgroundPlugins = msg.survivors
		m.backgroundTickRunning = false
		for _, issue := range msg.issues {
			m.recordBackgroundPluginIssue(issue)
		}
		var cmds []tea.Cmd
		if m.recoveryPluginActive {
			cmds = append(cmds, tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return recoveryTimeoutMsg{} }))
		}
		if m.backgroundTickQueued {
			m.backgroundTickQueued = false
			payload := append([]byte(nil), m.backgroundTickPayload...)
			m.backgroundTickPayload = nil
			cmds = append(cmds, m.tickBackgroundPluginsWithEvent(payload))
		}
		return m, tea.Batch(cmds...)

	case recoveryTimeoutMsg:
		if !m.recoveryPluginActive {
			return m, nil
		}
		m.recoveryPluginActive = false
		m.recoveryPluginName = ""
		m.appendBlock(block{
			kind: "system",
			body: "auto-recovery did not produce a compacted child session. Your blocked prompt is still in the editor; use /compact or session fork if you want to recover manually.",
		})
		m.renderBlocks()
		return m, nil

	case localHintMsg:
		// Async local-runner hint dispatched by ensureProvider's
		// error path. Append as a separate system block so the user
		// sees it arrive after the initial error.
		m.appendBlock(block{kind: "system", body: msg.body})
		m.renderBlocks()
		return m, nil

	case subagentEventMsg:
		m.recordSubagentEvent(msg.ev)
		return m, nil

	case toolResultMsg:
		// Async tool call completed — result arrives here so the UI
		// never blocks on long-running tools (e.g. bash sleep 30).
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

	case pluginApprovalRequestMsg:
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

	case pluginApprovalCancelMsg:
		if m.approval != nil && m.approval.response == msg.response {
			m.approval = nil
			m.approvalFocused = false
			m.approvalAllowSelected = true
			m.state = stateIdle
			m.renderBlocks()
		}
		return m, nil

	case toolTickMsg:
		m.toolMu.Lock()
		running := m.toolCancel != nil
		m.toolMu.Unlock()
		if !running {
			return m, nil
		}
		// Re-render tool blocks so the elapsed-time counter ticks.
		m.renderBlocks()
		return m, m.toolTickCmd()

	case pluginRunResultMsg:
		// /plugin:<name>-<ver> <tool> [args] finished. Render outcome
		// as a system block and leave conversation state untouched —
		// plugin invocations are side-channel and don't pollute the
		// turn log the LLM sees.
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

	case pluginForkMsg:
		if m.recoveryPluginActive && msg.plugin == m.recoveryPluginName {
			return m, m.adoptForkedSession(msg.childID, msg.seed)
		}
		// A plugin's session:fork capability just created a child
		// session. DESIGN invariant 4: this is user-visible by
		// default. Show both the new session id + the fork point +
		// a summary of the seed the plugin wrote into the child's
		// trace log.
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

	case btwResultMsg:
		if msg.errMsg != "" {
			m.appendBlock(block{
				kind: "system",
				body: fmt.Sprintf("btw error: %s", msg.errMsg),
			})
		} else {
			m.appendBlock(block{
				kind: "btw",
				body: msg.reply,
			})
		}
		m.renderBlocks()
		return m, nil

	case toolsExecutedMsg:
		m.annotateLastAssistantToolResults(msg.results)
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

	case tea.KeyMsg:
		// Ctrl+C closes any open modal popup. Esc still works (each
		// modal handles it internally), but adding Ctrl+C as a
		// secondary close key matches readline conventions and lets
		// the user dismiss popups without leaving home-row. Checked
		// before any modal-specific routing so it pre-empts the
		// modal's own keypress handling.
		if msg.Type == tea.KeyCtrlC && m.anyModalOpen() {
			m.closeAllModals()
			return m, nil
		}

		if m.showStatus {
			if action, ok := m.keys.TryPrefix(msg); ok {
				if action == keys.StatusView {
					m.showStatus = false
					m.layout()
				}
				return m, nil
			}
			if m.keys.Matches(msg, keys.SessionInterrupt) ||
				m.keys.Matches(msg, keys.TipsToggle) ||
				m.keys.Matches(msg, keys.StatusView) {
				m.showStatus = false
				m.layout()
			}
			return m, nil
		}

		if m.showHelp {
			if m.keys.Matches(msg, keys.SessionInterrupt) || m.keys.Matches(msg, keys.TipsToggle) {
				m.showHelp = false
				m.layout()
			}
			return m, nil
		}

		if m.approval != nil {
			if cmd, handled := m.handleApprovalKey(msg); handled {
				return m, cmd
			}
		}

		// Compaction confirmation: reuse the Approve / Deny keybindings
		// (y / n by default) so the UX matches tool-call approval.
		// EditSummary ('e') switches into an inline editor where the
		// user can revise the draft before accepting.
		if m.state == stateCompactionPending {
			if m.keys.Matches(msg, keys.Approve) {
				m.resolveCompaction(true)
				m.renderBlocks()
				return m, nil
			}
			if m.keys.Matches(msg, keys.Deny) {
				m.resolveCompaction(false)
				m.renderBlocks()
				return m, nil
			}
			if m.keys.Matches(msg, keys.EditSummary) {
				m.enterSummaryEdit()
				m.renderBlocks()
				return m, nil
			}
			// Any other key while pending is ignored — no accidental msgs
			// mutation while the user reads the summary.
			return m, nil
		}

		// Summary-editing state: Enter commits, Esc/Deny cancels. All
		// other keys flow to the editor so the user can type freely.
		if m.state == stateCompactionEditing {
			if m.keys.Matches(msg, keys.InputSubmit) {
				m.commitSummaryEdit()
				m.renderBlocks()
				return m, nil
			}
			if m.keys.Matches(msg, keys.Deny) {
				m.cancelSummaryEdit()
				m.renderBlocks()
				return m, nil
			}
			inputCmd, _ := m.input.Update(msg)
			return m, inputCmd
		}

		// Quit confirmation: y/Enter confirms, n/Esc cancels.
		if m.state == stateQuitConfirm {
			if m.keys.Matches(msg, keys.Approve) || msg.Type == tea.KeyEnter {
				return m, tea.Quit
			}
			if m.keys.Matches(msg, keys.Deny) || msg.Type == tea.KeyEsc {
				m.state = stateIdle
				m.renderBlocks()
				return m, nil
			}
			// Any other key is ignored.
			return m, nil
		}

		if m.slash.Visible {
			// Palette owns all keypresses while visible — keystrokes feed
			// its internal Query (so characters don't leak into the main
			// textarea beneath the modal).
			cmd, handled := m.slash.Update(msg)
			if handled {
				if !m.slash.Visible {
					m.slashInline = false
				}
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.slash.Selected(); sel != nil {
					m.slash.Close()
					m.slashInline = false
					return m, m.handleSlash(sel.Name)
				}
			}
			// Any other keys swallowed so they don't reach the input.
			return m, nil
		}

		if m.agentPick.Visible {
			cmd, handled := m.agentPick.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.agentPick.Selected(); sel != nil {
					m.agentPick.Close()
					if err := m.setAgentMode(sel.ID); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					m.layout()
					return m, nil
				}
			}
			return m, nil
		}

		// Fleet picker is modal — route keypresses and act on the
		// Result the picker emits. Esc / Ctrl+G fall through to
		// the SessionInterrupt handler below; Ctrl+C is handled at
		// the top of Update via closeAllModals.
		if m.fleetPicker != nil && m.fleetPicker.Visible {
			cmd, handled := m.fleetPicker.Update(msg)
			if !handled && (msg.Type == tea.KeyEsc) {
				m.fleetPicker.Close()
				m.layout()
				return m, nil
			}
			if m.fleetPicker.Out.Action != fleetpicker.ActionNone {
				out := m.fleetPicker.Out
				m.fleetPicker.Out = fleetpicker.Result{}
				switch out.Action {
				case fleetpicker.ActionView:
					if e, ok := m.fleet.Get(out.FleetID); ok && e.SessionID != "" {
						m.fleetPicker.Close()
						m.appendBlock(block{kind: "system",
							body: "fleet: switch to session " + e.SessionID + " — use `/session " + e.SessionID + "`"})
						m.renderBlocks()
					} else {
						m.appendBlock(block{kind: "system",
							body: "fleet: child session id not yet known (still spawning)"})
						m.renderBlocks()
					}
				case fleetpicker.ActionCancel:
					_ = m.fleet.Cancel(out.FleetID)
					m.fleetPicker.Refresh(m.fleet.List())
					m.appendBlock(block{kind: "system",
						body: "fleet: cancelled background agent " + shortFleetID(out.FleetID)})
					m.renderBlocks()
				case fleetpicker.ActionRemove:
					m.fleet.Remove(out.FleetID)
					m.fleetPicker.Refresh(m.fleet.List())
				}
			}
			if handled {
				return m, cmd
			}
			return m, nil
		}

		// Model picker is modal too — same routing pattern as palette.
		if m.modelPicker.Visible {
			if msg.Type == tea.KeyCtrlA {
				m.showSelectedProviderSetup()
				return m, nil
			}
			if msg.Type == tea.KeyCtrlF {
				if sel := m.modelPicker.Selected(); sel != nil {
					favorite := m.toggleModelFavorite(*sel)
					m.modelPicker.SetFavorite(sel.ID, sel.ProviderName, favorite)
					m.layout()
				}
				return m, nil
			}
			cmd, handled := m.modelPicker.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.modelPicker.Selected(); sel != nil {
					old := m.model
					oldProvider := m.providerName
					m.model = sel.ID

					// Provider switch: when the selected model came from
					// a different provider (typically a detected local
					// runner), the user almost certainly wants the
					// backend to switch too. Otherwise picking
					// "lmstudio · detected" still routes to anthropic
					// on the next prompt.
					providerSwitched := false
					if sel.ProviderName != "" && sel.ProviderName != oldProvider {
						m.providerName = sel.ProviderName
						m.provider = nil // force rebuild via buildProvider on next ensureProvider
						// Reset the token-counter probe so we re-check
						// against the new backend's capabilities.
						m.tokenCounterChecked = false
						providerSwitched = true
					}

					m.rememberModelSelection(*sel)
					m.modelPicker.Close()
					body := "model: " + old + " → " + m.model + "  (" + sel.Origin + ")"
					if providerSwitched {
						body += "\nprovider: " + oldProvider + " → " + m.providerName
					}
					if err := m.persistDefaultModel(m.providerName, m.model); err != nil {
						body += "\n" + err.Error()
					}
					m.appendBlock(block{kind: "system", body: body})
					m.layout()
					return m, nil
				}
			}
			return m, nil
		}

		if m.sessionPick.Visible {
			if m.sessionPick.Renaming() {
				if m.keys.Matches(msg, keys.InputSubmit) {
					target := m.sessionPick.Target()
					if err := m.renameSession(target.ID, m.sessionPick.RenameValue()); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					m.sessionPick.CancelAction()
					if err := m.openSessionPicker(); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					return m, nil
				}
				cmd, _ := m.sessionPick.Update(msg)
				return m, cmd
			}
			if m.sessionPick.Deleting() {
				if m.keys.Matches(msg, keys.InputSubmit) || yesKey(msg) {
					target := m.sessionPick.Target()
					if target.Current {
						return m, nil
					}
					if err := m.deleteSession(target.ID); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					m.sessionPick.CancelAction()
					if err := m.openSessionPicker(); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					return m, nil
				}
				if noKey(msg) {
					m.sessionPick.CancelAction()
					return m, nil
				}
				cmd, _ := m.sessionPick.Update(msg)
				return m, cmd
			}
			switch msg.Type {
			case tea.KeyCtrlN:
				m.sessionPick.Close()
				if err := m.createAndSwitchSession(); err != nil {
					m.appendBlock(block{kind: "system", body: err.Error()})
					m.renderBlocks()
				}
				return m, nil
			case tea.KeyCtrlR:
				m.sessionPick.BeginRename()
				m.layout()
				return m, nil
			case tea.KeyCtrlD:
				m.sessionPick.BeginDelete()
				m.layout()
				return m, nil
			case tea.KeyCtrlF:
				if sel := m.sessionPick.Selected(); sel != nil {
					m.sessionPick.Close()
					if err := m.forkAndSwitchSession(sel.ID); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
				}
				return m, nil
			}
			cmd, handled := m.sessionPick.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.sessionPick.Selected(); sel != nil {
					m.sessionPick.Close()
					if err := m.switchToSession(sel.ID); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					return m, nil
				}
			}
			return m, nil
		}

		if m.taskPick.Visible {
			cmd, handled := m.taskPick.Update(msg)
			if handled {
				if err := m.applyTaskCommand(cmd); err != nil {
					m.taskPick.SetNotice(err.Error())
				}
				m.layout()
				return m, nil
			}
			return m, nil
		}

		if m.themePick.Visible {
			cmd, handled := m.themePick.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				if sel := m.themePick.Selected(); sel != nil {
					m.themePick.Close()
					if sel.Current && sel.Mode == "custom" {
						return m, nil
					}
					if err := m.applyNamedTheme(sel.ID); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
					return m, nil
				}
			}
			return m, nil
		}

		// Filepicker popover owns navigation keys while visible so
		// Up/Down don't scroll the textarea and Tab/Enter accept the
		// highlighted path instead of inserting literal whitespace or
		// submitting a half-written prompt. Esc closes without
		// inserting. Anything else falls through so typing refines
		// the query naturally.
		if m.filePicker.Visible {
			if cmd, handled := m.filePicker.Update(msg); handled {
				return m, cmd
			}
			switch msg.Type {
			case tea.KeyEsc:
				m.filePicker.Close()
				return m, nil
			case tea.KeyTab:
				m.acceptFilePickerSelection()
				return m, nil
			case tea.KeyEnter:
				if m.filePicker.Selected() != "" {
					m.acceptFilePickerSelection()
					return m, nil
				}
			}
		}

		// Prefix-chord dispatch: ctrl+x <chord>, etc.
		// Placed after all modal checks so chords don't bypass overlays;
		// placed before flat keybindings so they take priority when
		// the prefix state is active.
		if action, ok := m.keys.TryPrefix(msg); ok {
			if action != "" {
				switch action {
				case keys.ModeToggleBtw:
					if m.mode == modeBTW {
						m.mode = modeDo
					} else {
						m.mode = modeBTW
					}
					m.layout()
				case keys.SidebarNarrower:
					m.resizeSidebar(-sidebarResizeStep)
					m.layout()
				case keys.SidebarWider:
					m.resizeSidebar(sidebarResizeStep)
					m.layout()
				case keys.SessionSwitch:
					if err := m.openSessionPicker(); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
				case keys.AgentSwitch:
					m.openAgentPicker()
					m.layout()
				case keys.ModelSwitch:
					m.openModelPicker()
					m.layout()
				case keys.SessionNew:
					if err := m.createAndSwitchSession(); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
				case keys.TaskView:
					if err := m.openTaskPicker(); err != nil {
						m.appendBlock(block{kind: "system", body: err.Error()})
						m.renderBlocks()
					}
				case keys.ThemeSwitch:
					m.openThemePicker()
					m.layout()
				case keys.StatusView:
					m.showStatus = true
					m.layout()
				case keys.ThinkingToggle:
					m.cycleThinkingDisplayMode()
					m.announceThinkingDisplayMode()
					m.layout()
				case keys.AppExit:
					m.state = stateQuitConfirm
					m.layout()
				}
			}
			return m, nil
		}

		switch {
		case m.keys.Matches(msg, keys.AppExit):
			m.state = stateQuitConfirm
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.SidebarToggle):
			m.sidebarOpen = !m.sidebarOpen
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.TipsToggle):
			// Gate `?` to empty input so a literal question mark inside a
			// prompt ("what's this?") inserts as text instead of popping
			// the help overlay. Ctrl+P remains reachable with content in
			// the editor; slash suggestions intentionally start from an
			// empty prompt.
			if m.input.Value() == "" {
				m.showHelp = true
				m.layout()
				return m, nil
			}

		case m.keys.Matches(msg, keys.CommandList):
			// Ctrl+P opens the command palette modal. The palette owns
			// its own search input — the main textarea is untouched.
			m.slashInline = false
			m.slash.Open()
			m.layout()
			return m, nil

		case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == '/' && m.input.Value() == "":
			m.slashInline = true
			m.slash.Open()
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.StatusView):
			m.showStatus = true
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.SessionInterrupt):
			// Esc / Ctrl+G — readline + Emacs canonical cancel.
			// Priority: clear queued prompt > cancel in-flight stream.
			// Mirrors the Ctrl+C (InputClear) behaviour for the
			// empty-input case so all three keys converge on a
			// consistent /cancel semantic.
			if m.queuedPrompt != "" {
				m.queuedPrompt = ""
				for i := len(m.blocks) - 1; i >= 0; i-- {
					if m.blocks[i].kind == "user" && m.blocks[i].queued {
						m.blocks = append(m.blocks[:i], m.blocks[i+1:]...)
						break
					}
				}
				m.appendBlock(block{kind: "system", body: "queued prompt cleared"})
				m.renderBlocks()
				return m, nil
			}
			if m.state == stateStreaming && m.streamCancel != nil {
				m.streamCancel()
				m.appendBlock(block{kind: "system", body: "turn cancelled"})
				m.renderBlocks()
				return m, nil
			}

		case m.keys.Matches(msg, keys.ForceQueue):
			// Alt+Enter — fire the queued prompt NOW. Cancels the
			// current turn (its existing cleanup drains the queue
			// and dispatches the queued prompt), so the next thing
			// the user sees is their just-submitted message running.
			if m.queuedPrompt == "" {
				m.appendBlock(block{kind: "system", body: "force-queue: no queued prompt"})
				m.renderBlocks()
				return m, nil
			}
			if m.state == stateStreaming && m.streamCancel != nil {
				m.streamCancel()
				m.appendBlock(block{kind: "system", body: "force-queue: cancelled current turn; queued prompt running"})
				m.renderBlocks()
			}
			return m, nil

		case m.keys.Matches(msg, keys.ToolExpand):
			m.toggleLastToolExpand()
			m.renderBlocks()
			return m, nil

		case m.keys.Matches(msg, keys.ModeToggle):
			if m.mode == modeDo {
				m.mode = modePlan
			} else {
				m.mode = modeDo
			}
			m.layout()
			return m, nil

		case m.keys.Matches(msg, keys.InputClear):
			// Ctrl+C: clear the chat input only. Cancel semantics
			// live on Esc / Ctrl+G (SessionInterrupt) and force-queue
			// on Alt+Enter (ForceQueue) — Ctrl+C does NOT touch the
			// in-flight stream or queued prompt anymore. The exit
			// key is Ctrl+D.
			//
			// The editor's own InputClear case (input/editor.go)
			// resets the textarea on falls-through; we don't return
			// here so that path runs.

		case m.keys.Matches(msg, keys.InputSubmit):
			if m.input.Value() == "" {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			tuiTrace("input submit", "state", int(m.state), "chars", len(text), "probe_pending", m.providerProbePending)
			// Enter while a turn is still streaming: queue the prompt
			// for after-done instead of silently dropping it (the old
			// behaviour) or abruptly cancelling (bad UX). The user's
			// block is appended to m.blocks IMMEDIATELY so they see
			// their message in the chat (dogfood-bug: silent queue
			// looked like a freeze). Only m.msgs add + startStream
			// wait for drain — the current turn is mid-stream and
			// must not see the new user message in its context window.
			if m.state == stateStreaming {
				// Slash commands bypass the queue — /clear, /compact,
				// /retry etc. are meta-commands users expect to act
				// immediately even mid-stream. Everything else
				// (regular prompts) gets queued for after-drain.
				if strings.HasPrefix(text, "/") {
					m.input.Reset()
					m.slash.Close()
					m.slashInline = false
					return m, m.handleSlash(text)
				}
				// EP-0033: supervisor lane — classify input and route accordingly.
				if m.cfg != nil && m.cfg.Supervisor.Enabled {
					switch classifyInput(text) {
					case supervisorAnswer:
						// Route to supervisor (btw) lane — answers immediately
						// from transcript context without queuing.
						m.input.History.Push(text)
						m.input.Reset()
						return m, m.startBtw(text)
					case supervisorInterrupt:
						// Cancel worker and queue the input.
						if m.streamCancel != nil {
							m.streamCancel()
						}
						m.appendBlock(block{kind: "system", body: "supervisor: interrupting worker — input queued for next turn"})
						m.queuedPrompt = text
						m.input.History.Push(text)
						m.input.Reset()
						m.renderBlocks()
						return m, nil
					case supervisorSteer:
						// Inject a steering note and queue.
						steer := "[supervisor steer] " + text
						m.appendBlock(block{kind: "btw", body: steer})
						m.queuedPrompt = text
						m.input.History.Push(text)
						m.input.Reset()
						m.renderBlocks()
						return m, nil
					default:
						// supervisorQueue: fall through to normal queue behaviour.
					}
				}
				m.queuedPrompt = text
				m.appendBlock(block{kind: "user", body: text, queued: true})
				m.renderBlocks()
				m.input.History.Push(text)
				m.input.Reset()
				return m, nil
			}
			if strings.HasPrefix(text, "/") {
				m.input.Reset()
				m.slash.Close()
				m.slashInline = false
				return m, m.handleSlash(text)
			}
			// EP-0038 §F: /session attach RW — route input to agent inbox.
			if m.attach.agentID != "" {
				agentID := m.attach.agentID
				if m.fleet != nil {
					if _, ok := m.fleet.Get(agentID); ok {
						m.appendBlock(block{kind: "user", body: text, source: "operator"})
						// Build a bridge adapter to inject the message.
						bridge := &runtime.FleetBridgeAdapter{
							Fleet:   m.fleet,
							Spawner: spawnerFunc(m.buildSubagentSpawner()),
							RootCtx: m.rootCtx,
						}
						_ = bridge.AgentSendMessage(m.rootCtx, agentID, text)
						m.appendBlock(block{kind: "system", body: fmt.Sprintf("→ sent to agent:%s", agentID[:min8(agentID)])})
					} else {
						m.attach = attachState{} // agent gone — auto-detach
						m.appendBlock(block{kind: "system", body: "agent no longer running — detached automatically"})
					}
				}
				m.input.History.Push(text)
				m.input.Reset()
				m.renderBlocks()
				return m, nil
			}
			// Budget hard-cap gate (same UX pattern as the context
			// hard-threshold). Draft text stays in input; user clears
			// the block with `/budget ack` which sets budgetAcked for
			// the remainder of the session.
			if m.budgetExceeded() {
				body := fmt.Sprintf(
					"cost $%.2f ≥ hard cap $%.2f — blocked. Continue with:\n"+
						"  · /budget ack — acknowledge and continue this session\n"+
						"  · edit [budget].hard_usd in config.toml to raise the cap",
					m.usage.CostUSD, m.budgetHardUSD)
				m.appendBlock(block{kind: "system", body: body})
				m.renderBlocks()
				return m, nil
			}
			// Hard-threshold gate (DESIGN §"Token accounting" 11.2.6).
			// Refuse to start a fresh turn once we're at/above the hard
			// bound — forces the user to /compact or fork before adding
			// more context. The draft text stays in the input so the
			// recovery flow doesn't lose it.
			if m.aboveHardThreshold() {
				if m.hasAutoCompactBackgroundPlugin() {
					m.recoveryPrompt = text
					m.recoveryPluginName = "auto-compact"
					m.recoveryPluginActive = true
					body := fmt.Sprintf(
						"context at %.0f%% (hard threshold %.0f%%) — running bundled auto-compact before replaying your prompt in a child session.",
						100*m.contextFraction(), 100*m.ctxHardThreshold)
					m.appendBlock(block{kind: "system", body: body})
					m.renderBlocks()
					return m, m.tickBackgroundPluginsWithEvent(m.contextOverflowEvent(text))
				}
				body := fmt.Sprintf(
					"context at %.0f%% (hard threshold %.0f%%) — blocked. Recover with:\n"+
						"  · /compact — user-confirmed in-TUI summarisation\n"+
						"  · stado session fork <id> --at turns/<N> — branch from an earlier turn",
					100*m.contextFraction(), 100*m.ctxHardThreshold)
				// Offer auto-compact specifically when it's installed —
				// the user doesn't have to remember the exact plugin-id
				// string; we've already found one on disk.
				if ac := m.installedAutoCompact(); ac != "" {
					body += fmt.Sprintf("\n  · /plugin:%s compact — automated compact + fork via the auto-compact plugin", ac)
				}
				m.appendBlock(block{kind: "system", body: body})
				m.renderBlocks()
				return m, nil
			}
			if m.provider == nil && m.providerProbePending && m.providerName == "" {
				m.queuedPrompt = text
				m.appendBlock(block{kind: "user", body: text, queued: true})
				m.renderBlocks()
				m.input.History.Push(text)
				m.input.Reset()
				tuiTrace("submit queued behind startup provider probe", "chars", len(text))
				return m, nil
			}
			m.input.History.Push(text)
			m.input.Reset()
			if m.mode == modeBTW {
				return m, m.startBtw(text)
			}
			m.appendUser(text)
			m.renderBlocks()
			return m, m.startStream()
		}
	}

	cmd, _ := m.vp, tea.Cmd(nil)
	_ = cmd

	inputCmd, _ := m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	// Re-scan for an active @-trigger after every editor keypress.
	// Typing '@' opens the picker; typing past the word boundary
	// (space, newline, or moving the cursor away) closes it.
	m.updateFilePickerFromInput()

	// Scroll messages viewport
	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) toggleLastToolExpand() {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		switch {
		case m.blocks[i].kind == "assistant" && strings.TrimSpace(m.blocks[i].details) != "":
			m.blocks[i].expanded = !m.blocks[i].expanded
			return
		case m.blocks[i].kind == "tool":
			m.blocks[i].expanded = !m.blocks[i].expanded
			return
		}
	}
}

func (m *Model) resolveApproval(allow bool) tea.Cmd {
	req := m.approval
	m.approval = nil
	m.approvalFocused = false
	m.approvalAllowSelected = true
	if req == nil {
		m.state = stateIdle
		m.renderBlocks()
		return nil
	}
	if req.response != nil {
		select {
		case req.response <- allow:
		default:
		}
	}
	m.state = stateIdle
	m.renderBlocks()
	return nil
}

// activeAtTrigger returns (atPos, query, ok) when the input cursor
// sits inside an @-prefixed word. atPos is the byte index of '@' in
// the buffer; query is everything between '@' and the cursor. The
// trigger is only recognised when '@' is at the start of input or
// directly preceded by whitespace, so email addresses and package
// references don't accidentally fire the picker.
func (m *Model) activeAtTrigger() (atPos int, query string, ok bool) {
	val := m.input.Value()
	cursor := m.input.CursorOffset()
	if cursor < 0 || cursor > len(val) {
		return 0, "", false
	}
	for i := cursor - 1; i >= 0; i-- {
		r := val[i]
		if r == '@' {
			if i == 0 || val[i-1] == ' ' || val[i-1] == '\n' || val[i-1] == '\t' {
				return i, val[i+1 : cursor], true
			}
			return 0, "", false
		}
		if r == ' ' || r == '\n' || r == '\t' {
			return 0, "", false
		}
	}
	return 0, "", false
}

// updateFilePickerFromInput inspects the current editor state and
// shows/hides/refreshes the file picker accordingly. Called after
// every keypress the editor processes. No-op when the picker's
// visibility is already correct for the buffer state.
func (m *Model) updateFilePickerFromInput() {
	atPos, query, ok := m.activeAtTrigger()
	if !ok {
		if m.filePicker.Visible {
			m.filePicker.Close()
		}
		return
	}
	if !m.filePicker.Visible || m.filePicker.Anchor != atPos {
		cwd := m.cwd
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		m.filePicker.OpenWithItems(cwd, atPos, m.filePickerContextItems())
	}
	m.filePicker.SetQuery(query)
}

func yesKey(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	return msg.Runes[0] == 'y' || msg.Runes[0] == 'Y'
}

func noKey(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	return msg.Runes[0] == 'n' || msg.Runes[0] == 'N'
}

func (m *Model) appendSubagentNotice(content string) {
	var res subagent.Result
	if err := json.Unmarshal([]byte(content), &res); err != nil || res.ChildSession == "" {
		return
	}
	m.recordSubagentResult(res)
	status := res.Status
	if status == "" {
		status = "completed"
	}
	body := fmt.Sprintf("spawn_agent %s → %s", status, res.ChildSession)
	if res.Error != "" {
		body += "\n  error: " + trimSeed(res.Error, 160)
	}
	if res.Worktree != "" {
		body += "\n  worktree: " + res.Worktree
	}
	if len(res.ChangedFiles) > 0 {
		body += fmt.Sprintf("\n  changed: %d file(s)", len(res.ChangedFiles))
	}
	if len(res.ScopeViolations) > 0 {
		body += fmt.Sprintf("\n  scope violations: %d", len(res.ScopeViolations))
	}
	body += "\n  attach:  stado session attach " + res.ChildSession
	if len(res.ChangedFiles) > 0 {
		parentID := "<parent-session-id>"
		if m.session != nil && m.session.ID != "" {
			parentID = m.session.ID
		}
		adopt := "\n  adopt:   stado session adopt " + parentID + " " + res.ChildSession
		if res.ForkTree != "" {
			adopt += " --fork-tree " + res.ForkTree
		}
		adopt += " --apply"
		body += adopt
	}
	m.appendBlock(block{kind: "system", body: body})
}

// acceptFilePickerSelection replaces the @<query> fragment in the
// input buffer with the highlighted path, followed by a space so the
// user can keep typing. Closes the picker. No-op when nothing is
// selected — the caller falls through to the normal submit/tab path.
func (m *Model) acceptFilePickerSelection() {
	item, ok := m.filePicker.SelectedItem()
	if !ok {
		return
	}
	val := m.input.Value()
	anchor := m.filePicker.Anchor
	cursor := m.input.CursorOffset()
	if anchor < 0 || anchor > len(val) || cursor < anchor || cursor > len(val) {
		m.filePicker.Close()
		return
	}
	if item.Kind == filepicker.KindAgent {
		if err := m.setAgentMode(item.ID); err == nil {
			m.input.SetValue(consumeMentionDraft(val, anchor, cursor))
			m.filePicker.Close()
			m.layout()
			return
		}
	}
	if item.Kind == filepicker.KindSession {
		if strings.TrimSpace(val[:anchor]) == "" && strings.TrimSpace(val[cursor:]) == "" {
			m.input.SetValue("")
			m.filePicker.Close()
			if err := m.switchToSession(item.ID); err != nil {
				m.appendBlock(block{kind: "system", body: err.Error()})
				m.renderBlocks()
			}
			return
		}
	}
	if item.Kind == filepicker.KindSkill {
		if err := m.injectSkill(item.ID); err != nil {
			m.appendBlock(block{kind: "system", body: err.Error()})
			m.renderBlocks()
		} else {
			m.input.SetValue(consumeMentionDraft(val, anchor, cursor))
		}
		m.filePicker.Close()
		m.layout()
		return
	}
	insert := item.Insert
	if insert == "" {
		insert = item.Display
	}
	newVal := val[:anchor] + insert + " " + val[cursor:]
	m.input.SetValue(newVal)
	m.filePicker.Close()
}
