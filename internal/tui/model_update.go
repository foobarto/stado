package tui

// Update is the bubbletea Update entrypoint. The implementation is a
// thin dispatcher: every message family has its own handler in a
// handler_<family>.go sibling file. The dispatcher routes by message
// type and falls through (for KeyMsg with handled=false) to the
// editor / viewport update path.
//
// Handler files (each in package tui):
//   - handler_lifecycle.go   — window/title/log/loop/monitor/recovery
//   - handler_stream.go      — provider streaming + btw answers
//   - handler_tools.go       — tool/plugin events (approval, choice,
//                              fork, run-result, tool-tick, etc.)
//   - handler_picker_response.go — picker-active KeyMsg dispatch
//   - handler_input.go       — KeyMsg + MouseMsg (calls onPickerKey
//                              when a picker is open)

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tui/filepicker"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return onWindowSize(m, msg)
	case titleTickMsg:
		return onTitleTick(m, msg)

	case streamEventMsg:
		return onStreamEvent(m, msg)
	case streamBatchMsg:
		return onStreamBatch(m, msg)
	case streamTickMsg:
		return onStreamTick(m, msg)
	case streamErrorMsg:
		return onStreamError(m, msg)
	case streamDoneMsg:
		return onStreamDone(m, msg)
	case btwResultMsg:
		return onBtwResult(m, msg)

	case logTailMsg:
		return onLogTail(m, msg)
	case localFallbackReadyMsg:
		return onLocalFallbackReady(m, msg)
	case loopTickMsg:
		return onLoopTick(m, msg)
	case monitorLinesMsg:
		return onMonitorLines(m, msg)
	case monitorDoneMsg:
		return onMonitorDone(m, msg)
	case backgroundTickResultMsg:
		return onBackgroundTickResult(m, msg)
	case recoveryTimeoutMsg:
		return onRecoveryTimeout(m, msg)
	case localHintMsg:
		return onLocalHint(m, msg)
	case subagentEventMsg:
		return onSubagentEvent(m, msg)

	case toolResultMsg:
		return onToolResult(m, msg)
	case toolsExecutedMsg:
		return onToolsExecuted(m, msg)
	case toolTickMsg:
		return onToolTick(m, msg)
	case pluginPrintMsg:
		return onPluginPrint(m, msg)
	case pluginApprovalRequestMsg:
		return onPluginApprovalRequest(m, msg)
	case pluginApprovalCancelMsg:
		return onPluginApprovalCancel(m, msg)
	case pluginChoiceRequestMsg:
		return onPluginChoiceRequest(m, msg)
	case pluginChoiceCancelMsg:
		return onPluginChoiceCancel(m, msg)
	case pluginRunResultMsg:
		return onPluginRunResult(m, msg)
	case pluginForkMsg:
		return onPluginFork(m, msg)

	case tea.MouseMsg:
		return onMouse(m, msg)

	case tea.KeyMsg:
		if model, cmd, handled := onKey(m, msg); handled {
			return model, cmd
		}
		// Falls through: the InputClear case + any KeyMsg that didn't
		// match a flat keybinding. The editor processes typing /
		// Ctrl+C clear; the viewport processes scroll keys.
	}

	// Fall-through path. For unhandled KeyMsg, the editor consumes the
	// keystroke (typing → buffer; Ctrl+C → clear via the editor's own
	// InputClear case in input/editor.go), then we re-scan for an
	// active @-trigger to keep the file picker in sync, then forward
	// to the viewport for any scroll keys.
	var cmds []tea.Cmd

	inputCmd, _ := m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	// Re-scan for an active @-trigger after every editor keypress.
	// Typing '@' opens the picker; typing past the word boundary
	// (space, newline, or moving the cursor away) closes it.
	m.updateFilePickerFromInput()

	// Scroll messages viewport.
	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

// toggleLastToolExpand toggles the focused expandable block's
// expansion. When no block is focused, falls back to the latest
// expandable block. EP-N/A — supports expanding older tool calls.
func (m *Model) toggleLastToolExpand() {
	// Honour focus when set; the user navigated here deliberately.
	if m.focusedBlockIdx >= 0 && m.focusedBlockIdx < len(m.blocks) {
		blk := &m.blocks[m.focusedBlockIdx]
		if isExpandableBlock(*blk) {
			blk.expanded = !blk.expanded
			return
		}
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if isExpandableBlock(m.blocks[i]) {
			m.blocks[i].expanded = !m.blocks[i].expanded
			return
		}
	}
}

// isExpandableBlock reports whether a block has expand/collapse state
// the user might want to toggle (tool blocks, plus assistant blocks
// that have hidden details).
func isExpandableBlock(b block) bool {
	switch b.kind {
	case "tool":
		return true
	case "assistant":
		return strings.TrimSpace(b.details) != ""
	}
	return false
}

// focusPrevExpandable moves the focused block one step earlier in the
// conversation, skipping non-expandable blocks. With no current focus,
// jumps to the latest expandable block.
func (m *Model) focusPrevExpandable() {
	if len(m.blocks) == 0 {
		return
	}
	start := len(m.blocks) - 1
	if m.focusedBlockIdx >= 0 {
		start = m.focusedBlockIdx - 1
	}
	for i := start; i >= 0; i-- {
		if isExpandableBlock(m.blocks[i]) {
			m.clearFocus()
			m.focusedBlockIdx = i
			m.blocks[i].focused = true
			m.invalidateBlockCache(i)
			return
		}
	}
	// No earlier expandable — keep current focus.
}

// focusNextExpandable moves the focused block one step later. When the
// user is already on the latest expandable, clears focus (so the next
// ToolExpand falls through to "latest").
func (m *Model) focusNextExpandable() {
	if len(m.blocks) == 0 {
		return
	}
	if m.focusedBlockIdx < 0 {
		return // nothing focused, nothing to advance
	}
	for i := m.focusedBlockIdx + 1; i < len(m.blocks); i++ {
		if isExpandableBlock(m.blocks[i]) {
			m.clearFocus()
			m.focusedBlockIdx = i
			m.blocks[i].focused = true
			m.invalidateBlockCache(i)
			return
		}
	}
	// Past the last expandable — clear focus so ToolExpand falls
	// back to "latest".
	m.clearFocus()
}

// clearFocus removes the focus marker from the previously-focused block.
func (m *Model) clearFocus() {
	if m.focusedBlockIdx >= 0 && m.focusedBlockIdx < len(m.blocks) {
		m.blocks[m.focusedBlockIdx].focused = false
		m.invalidateBlockCache(m.focusedBlockIdx)
	}
	m.focusedBlockIdx = -1
}

// blockAtContentLine returns the index of the block occupying the
// given content-line in the rendered viewport, or -1 if no block
// overlaps that line. Used by the mouse click handler.
func (m *Model) blockAtContentLine(line int) int {
	for _, r := range m.blockLineRanges {
		if line >= r.start && line < r.end {
			return r.blockIdx
		}
	}
	return -1
}

// handleMessagesClick processes a left-button click on the messages
// viewport area. Maps the click to a block, sets focus, and toggles
// expansion if the block is expandable. Returns true when the click
// consumed something (caller skips default scroll handling).
func (m *Model) handleMessagesClick(msgX, msgY int) bool {
	// Single-view: vp starts at row 0 of the left column. Split-view:
	// activityVP fills the top, then a separator row, then vp.
	vpTop := 0
	if m.splitView {
		vpTop = m.activityVP.Height + 1 // +1 for separator row
	}
	if msgY < vpTop || msgY >= vpTop+m.vp.Height {
		return false
	}
	// Sidebar lives to the right of vp. Reject clicks past the vp
	// width — those land on the sidebar, not the conversation.
	if msgX >= m.vp.Width {
		return false
	}
	contentLine := m.vp.YOffset + (msgY - vpTop)
	idx := m.blockAtContentLine(contentLine)
	if idx < 0 {
		return false
	}
	if !isExpandableBlock(m.blocks[idx]) {
		return false
	}
	// Move focus to the clicked block + toggle its expansion.
	m.clearFocus()
	m.focusedBlockIdx = idx
	m.blocks[idx].focused = true
	m.blocks[idx].expanded = !m.blocks[idx].expanded
	m.invalidateBlockCache(idx)
	return true
}

// resolveChoice closes the choice drawer with a positive answer
// (Enter pressed). Sends ChoiceResponse{Selected, InputValue,
// Cancelled=false} down the bridge channel and clears drawer state.
// inputValue is the F10 typed text from the chosen option's input
// field; "" when the chosen option had no input (pre-F10 behaviour).
func (m *Model) resolveChoice(selected []string, inputValue string, cancelled bool) tea.Cmd {
	req := m.choice
	m.choice = nil
	m.choiceFocused = false
	m.choiceCursor = 0
	m.choiceMarked = nil
	m.choiceInputs = nil
	m.choiceValidationErr = ""
	m.state = stateIdle
	if req != nil && req.response != nil {
		select {
		case req.response <- pluginRuntime.ChoiceResponse{
			Selected:   selected,
			InputValue: inputValue,
			Cancelled:  cancelled,
		}:
		default:
		}
	}
	m.renderBlocks()
	return nil
}

// resolveChoiceCancel closes the drawer with cancelled=true (Esc).
func (m *Model) resolveChoiceCancel() tea.Cmd {
	return m.resolveChoice(nil, "", true)
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
